package audiorendition

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// ReconcileInterval is how often [Service.Run] rescans every current audio
// asset for a missing, still-rendering (left over from a crash), or failed
// rendition, in addition to running once at startup and on every
// [Service.Nudge]. This is the "coordinator startup or reconcile pass"
// that transcodes an existing audio asset uploaded before this feature
// shipped, with no operator action.
const ReconcileInterval = 5 * time.Minute

// Store is what [Service] needs from internal/coordinator/store.
type Store interface {
	ListAudioAssetContentHashesNeedingRendition(ctx context.Context) ([]string, error)
	SetAudioRenditionRendering(ctx context.Context, originalContentHash string) error
	SetAudioRenditionReady(ctx context.Context, originalContentHash string, ready store.AudioRenditionReady) error
	SetAudioRenditionFailed(ctx context.Context, originalContentHash, reason string) error
}

// Service builds a 48 kHz/16-bit/stereo WAV rendition of every current
// audio asset, off the request path (ADR-028 decision 4's byte store stays
// the only place asset bytes live; this service reads and writes it
// exactly like the upload handler does, never SQLite). It never touches
// the operator's original upload: the rendition is a second, separately
// content-addressed blob, referenced by the original's content hash.
type Service struct {
	st      Store
	backend assetstore.Backend
	logger  *slog.Logger
	nudge   chan struct{}
}

// NewService constructs a [Service]. logger must not be nil.
func NewService(st Store, backend assetstore.Backend, logger *slog.Logger) *Service {
	return &Service{st: st, backend: backend, logger: logger, nudge: make(chan struct{}, 1)}
}

// Nudge requests an immediate reconcile pass rather than waiting for the
// next [ReconcileInterval] tick. Coalesced: multiple calls before the pass
// starts trigger only one.
func (s *Service) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Run reconciles once immediately, then again on every tick or [Service.
// Nudge], until ctx is done. It never returns except when ctx is done.
func (s *Service) Run(ctx context.Context) {
	for {
		s.reconcile(ctx)
		if ctx.Err() != nil {
			return
		}

		timer := time.NewTimer(ReconcileInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.nudge:
			timer.Stop()
		}
	}
}

// reconcile renders every current audio asset content hash that has no
// ready rendition yet.
func (s *Service) reconcile(ctx context.Context) {
	hashes, err := s.st.ListAudioAssetContentHashesNeedingRendition(ctx)
	if err != nil {
		s.logger.Error("audio rendition: list audio assets needing a rendition", "error", err)
		return
	}
	for _, hash := range hashes {
		if ctx.Err() != nil {
			return
		}
		s.renderOne(ctx, hash)
	}
}

// renderOne builds and stores one content hash's rendition: an asset
// uploaded before this feature shipped, or one whose previous attempt was
// left "rendering" by a crash or failed. A refusal here never touches the
// original asset row or its expected-set entry, see
// [assetsync.ExpectedAssetsForNode]'s own rule that an asset with no ready
// rendition keeps naming the original.
func (s *Service) renderOne(ctx context.Context, originalHash string) {
	if err := s.st.SetAudioRenditionRendering(ctx, originalHash); err != nil {
		s.logger.Error("audio rendition: mark rendering", "content_hash", originalHash, "error", err)
		return
	}

	rc, _, err := s.backend.Open(ctx, originalHash)
	if err != nil {
		s.fail(ctx, originalHash, fmt.Sprintf("open stored asset: %v", err))
		return
	}
	defer func() { _ = rc.Close() }()

	format, err := DetectFormat(rc)
	if err != nil {
		s.fail(ctx, originalHash, err.Error())
		return
	}
	if _, err := rc.Seek(0, io.SeekStart); err != nil {
		s.fail(ctx, originalHash, fmt.Sprintf("rewind stored asset: %v", err))
		return
	}

	rendition, err := Render(rc, format)
	if err != nil {
		s.fail(ctx, originalHash, err.Error())
		return
	}

	blob, err := s.backend.Put(ctx, bytes.NewReader(rendition.WAV), int64(len(rendition.WAV)))
	if err != nil {
		s.fail(ctx, originalHash, fmt.Sprintf("store rendition: %v", err))
		return
	}

	if err := s.st.SetAudioRenditionReady(ctx, originalHash, store.AudioRenditionReady{
		ContentHash:    blob.ContentHash,
		SizeBytes:      blob.SizeBytes,
		DurationMillis: rendition.DurationMillis,
		Format:         RenditionFormat,
	}); err != nil {
		s.logger.Error("audio rendition: record ready rendition", "content_hash", originalHash, "error", err)
		return
	}
	s.logger.Info("audio rendition: transcoded", "content_hash", originalHash,
		"rendition_content_hash", blob.ContentHash, "duration_ms", rendition.DurationMillis)
}

// fail records originalHash's rendition as failed. The original asset is
// still served and still expected from every node: a transcode failure
// degrades node prepare time back to what it is today, never the show.
func (s *Service) fail(ctx context.Context, originalHash, reason string) {
	if err := s.st.SetAudioRenditionFailed(ctx, originalHash, reason); err != nil {
		s.logger.Error("audio rendition: record failed rendition", "content_hash", originalHash, "error", err)
	}
	s.logger.Warn("audio rendition: transcode failed; the original file is still served to nodes",
		"content_hash", originalHash, "reason", reason)
}
