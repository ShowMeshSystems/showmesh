package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fseq"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F3: FSEQ duration resolution (RESTING-MODE.md §6.1, rule
// 1) and the FPP-facing subset of §13 readiness, built on seam F0's own
// findings (bench/fpp-multisync/captures/trackf-f0/SUMMARY.md).

// nightAssetLister is the one method this file needs to look up an
// asset by identity — satisfied by both [Dependencies.Assets] (the
// night loop's own ordinary, non-transactional reads) and *store.Tx
// (nightRunReadinessTx's own call, which runs INSIDE the gated command's
// AuditedWrite transaction and would deadlock this coordinator's
// single-connection pool — store.go's own guardNotInTx — if it read
// through the non-Tx Store method instead). Both already satisfy this
// signature structurally; no adapter type is needed.
type nightAssetLister interface {
	ListAssets(ctx context.Context, filter store.AssetFilter) ([]store.AssetRecord, error)
}

// nightResolveCurrentAsset finds the current (non-superseded) asset for
// (show, sequence, target), mirroring nightSessionAssetCurrent's own
// filter (nightsession.go) but returning the full record this seam needs
// rather than a bool.
func nightResolveCurrentAsset(ctx context.Context, lister nightAssetLister, show, sequence, target string) (store.AssetRecord, bool, error) {
	recs, err := lister.ListAssets(ctx, store.AssetFilter{ShowID: show, SequenceID: sequence, NodeID: target})
	if err != nil {
		return store.AssetRecord{}, false, err
	}
	for _, r := range recs {
		if r.SupersededAt == nil {
			return r, true, nil
		}
	}
	return store.AssetRecord{}, false, nil
}

// nightAssetFSEQDurationResult is [nightResolveFSEQDuration]'s outcome:
// exactly one of (DurationMS>0, Reason) is meaningful. Reason is a
// readiness failure, never a Go error — resolving no duration is an
// expected, common outcome this seam must report cleanly, not something
// that belongs in an error return (rule 1: "A duration that cannot be
// read, or reads as zero, is a readiness failure with a reason, never a
// boundary at time zero and never a guess").
type nightAssetFSEQDurationResult struct {
	DurationMS int64
	Filename   string
	Reason     string
	// Cause is the error behind Reason. It is logged, never shown.
	Cause error
}

// nightResolveFSEQDuration is rule 1's own arithmetic:
// FrameCount()*StepTimeMS(), read from the exact ADR-028-identified asset
// (show, sequence, target) — never a filename lookup. FrameCount()==0 and
// StepTimeMS()==0 are distinguished explicitly (F0 §1's own finding: both
// collapse to a duration of 0 and are otherwise indistinguishable), so the
// returned Reason names which one failed.
func nightResolveFSEQDuration(ctx context.Context, deps Dependencies, lister nightAssetLister, show string, ref config.NightSessionAssetRef) nightAssetFSEQDurationResult {
	rec, ok, err := nightResolveCurrentAsset(ctx, lister, show, ref.Sequence, ref.Target)
	if err != nil {
		return nightAssetFSEQDurationResult{Cause: err, Reason: "The coordinator could not look up the resting sequence file. Check the coordinator log."}
	}
	if !ok {
		return nightAssetFSEQDurationResult{Reason: fmt.Sprintf("No sequence file is uploaded for %s on %s. Upload it.", ref.Sequence, ref.Target)}
	}
	if rec.MediaType != "fseq" {
		return nightAssetFSEQDurationResult{Reason: fmt.Sprintf("The file for resting sequence %s is not a sequence file. Upload a sequence file.", ref.Sequence)}
	}

	f, cleanup, err := nightOpenFSEQAsset(ctx, deps, rec)
	if err != nil {
		return nightAssetFSEQDurationResult{Cause: err, Reason: "The resting sequence file could not be read. Upload it again."}
	}
	defer cleanup()

	frameCount := f.FrameCount()
	stepTimeMS := f.StepTimeMS()
	switch {
	case frameCount == 0 && stepTimeMS == 0:
		return nightAssetFSEQDurationResult{Reason: "The resting sequence file has no frames and no frame rate. Export it again.", Filename: rec.RuntimeFilename}
	case frameCount == 0:
		return nightAssetFSEQDurationResult{Reason: "The resting sequence file has no frames, so it has no length. Export it again.", Filename: rec.RuntimeFilename}
	case stepTimeMS == 0:
		return nightAssetFSEQDurationResult{Reason: "The resting sequence file has no frame rate, so it has no length. Export it again.", Filename: rec.RuntimeFilename}
	}
	durationMS := int64(frameCount) * int64(stepTimeMS)
	return nightAssetFSEQDurationResult{DurationMS: durationMS, Filename: rec.RuntimeFilename}
}

// nightOpenFSEQAsset streams rec's bytes into a temp file (pkg/fseq.Open
// needs a seekable *os.File) and opens it. The returned cleanup closes and
// removes the temp file; always call it.
func nightOpenFSEQAsset(ctx context.Context, deps Dependencies, rec store.AssetRecord) (*fseq.File, func(), error) {
	rc, size, err := deps.AssetBackend.Open(ctx, rec.StorageKey)
	if err != nil {
		return nil, func() {}, err
	}
	defer func() { _ = rc.Close() }()
	if size != rec.SizeBytes {
		return nil, func() {}, fmt.Errorf("stored blob is %d bytes but the recorded size is %d bytes", size, rec.SizeBytes)
	}

	tmp, err := os.CreateTemp("", "showmesh-night-fseq-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	if _, err := io.Copy(tmp, io.LimitReader(rc, size)); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	// tmp is closed above so fseq.Open can reopen it by name (its only
	// entry point); cleanup still removes the file by name.
	f, err := fseq.Open(tmp.Name())
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return f, func() {
		_ = f.Close()
		_ = os.Remove(tmp.Name())
	}, nil
}

// --- FPP playlist definition read (F0 §2: readable while idle, distinct
// from running-state observation; RESTING-MODE.md §13's "readiness may
// never claim playlist contents from running-state observations alone"). ---

const nightPlaylistReadTimeout = 5 * time.Second

// nightPlaylistItem is one entry of a decoded playlist's mainPlaylist —
// only the fields F0 §2 established readiness needs.
type nightPlaylistItem struct {
	Type         string `json:"type"`
	SequenceName string `json:"sequenceName"`
	MediaName    string `json:"mediaName"`
}

type nightPlaylistDefinition struct {
	Name         string              `json:"name"`
	MainPlaylist []nightPlaylistItem `json:"mainPlaylist"`
}

// nightReadPlaylistDefinition performs the F0 §2 idle GET,
// /api/playlist/:name, against endpoint. It never follows a redirect
// (fppcommand.refuseRedirects' own reasoning, duplicated here rather than
// shared for the identical decoupling reason fppcommand's own doc comment
// gives: this file's own client must not trust a caller-supplied
// *http.Client's CheckRedirect any more than that package does).
func nightReadPlaylistDefinition(ctx context.Context, endpoint, name string) (nightPlaylistDefinition, error) {
	u := strings.TrimSuffix(endpoint, "/") + "/api/playlist/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nightPlaylistDefinition{}, err
	}
	client := &http.Client{
		Timeout: nightPlaylistReadTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nightPlaylistDefinition{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nightPlaylistDefinition{}, fmt.Errorf("GET %s: unexpected status %s", u, resp.Status)
	}
	var def nightPlaylistDefinition
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&def); err != nil {
		return nightPlaylistDefinition{}, fmt.Errorf("decode playlist definition: %w", err)
	}
	return def, nil
}

// nightCheckRestingPlaylistShape is §6.1's "exactly one FSEQ item and no
// FPP audio item" check, and §13's own bullet — a real read/readiness
// check per F0 §2, never an unknown standing in for "not implemented".
// unreachable (the GET itself failing) is reported unknown, not failed:
// this coordinator genuinely cannot tell the shape from here, which is
// different from having checked and found it wrong. This function
// verifies SHAPE only (item count, type, no audio association) — see
// [nightCheckRestingAssetExactVariant] for the separate, and separately
// reported, question of whether the running host is playing THESE exact
// bytes.
// singleItem is false for the end-of-night playlist: no boundary is
// derived from it, so a multi-item repeating loop is a legitimate shape
// there and must not be reported as a failure.
func nightCheckRestingPlaylistShape(ctx context.Context, endpoint, namePrefix, playlistName string, singleItem bool) nightReadinessCheck {
	name := namePrefix + ":playlist-shape:" + playlistName
	def, err := nightReadPlaylistDefinition(ctx, endpoint, playlistName)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err, reason: fmt.Sprintf(nightPlaylistUnreadReason, playlistName)}
	}
	switch {
	case singleItem && len(def.MainPlaylist) != 1:
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("Playlist %s must hold exactly one sequence, and it holds %d. Edit the playlist in FPP.", playlistName, len(def.MainPlaylist))}
	case len(def.MainPlaylist) == 0:
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(nightPlaylistEmptyReason, playlistName)}
	}
	for i, item := range def.MainPlaylist {
		if item.Type != "sequence" || item.MediaName != "" {
			return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("Item %d of playlist %s is not a sequence with no audio. Fix it in FPP.", i+1, playlistName)}
		}
		if item.SequenceName == "" {
			return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("Item %d of playlist %s names no sequence. Fix it in FPP.", i+1, playlistName)}
		}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

// nightCheckRestingAssetExactVariant reports whether the exact deployed
// FSEQ variant is confirmed running — always not_verifiable: FPP's
// playlist read exposes only a filename, never a content hash, so this
// can never be confirmed by this build. Kept separate from the shape
// check rather than folded into its reason string.
func nightCheckRestingAssetExactVariant(namePrefix, playlistName string) nightReadinessCheck {
	return nightReadinessCheck{
		name: namePrefix + ":asset-exact-variant:" + playlistName, health: nightCheckStateNotVerifiable,
		reason: "FPP reports only a file name, so the coordinator cannot confirm it holds this exact file.",
	}
}

// nightCheckShowPlaylistPresent is §13's "show playlist present and not
// unexpectedly busy" bullet's presence half. The "not unexpectedly busy"
// half is enforced at dispatch time by the startPlaylist primitive's own
// ifBusy=refuse guard (fppcommand_primitives.go), not here: busy is
// playback STATE, which changes between a readiness check and the moment
// start-night actually dispatches, so restating it as a readiness
// snapshot would be exactly the stale-evidence risk rule 5 exists to
// avoid. This check states that scope explicitly rather than silently
// narrowing what §13 asks for.
func nightCheckShowPlaylistPresent(ctx context.Context, endpoint, playlistName string) nightReadinessCheck {
	name := "show:playlist-present:" + playlistName
	def, err := nightReadPlaylistDefinition(ctx, endpoint, playlistName)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), cause: err, reason: fmt.Sprintf(nightPlaylistUnreadReason, playlistName)}
	}
	if len(def.MainPlaylist) == 0 {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(nightPlaylistEmptyReason, playlistName)}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

// nightCheckRestingAssetDuration is §6.1/§13's duration/cue-offset bullet:
// a parseable, non-zero duration read from the pinned asset, independent
// of FPP (rule 1). cueOffsetsWithinRange, when the caller has already
// resolved a duration, additionally checks every configured enterShow/
// enterResting cue offset fits inside the usable timeline (§13: "cue
// offsets within the usable timeline").
func nightCheckRestingAssetDuration(ctx context.Context, deps Dependencies, lister nightAssetLister, show string, ref config.NightSessionAssetRef, cueOffsetsMs []int) nightReadinessCheck {
	name := "resting:asset-duration"
	res := nightResolveFSEQDuration(ctx, deps, lister, show, ref)
	if res.Reason != "" {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: res.Reason, cause: res.Cause}
	}
	for _, off := range cueOffsetsMs {
		if off < 0 && int64(-off) > res.DurationMS {
			return nightReadinessCheck{name: name, health: nightHealthFailed(),
				reason: fmt.Sprintf("A cue is set to run %s before the show, but the resting sequence is only %s long. Shorten the offset.",
					nightSecondsWords(int64(-off)), nightSecondsWords(res.DurationMS))}
		}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}

func nightHealthHealthy() nightCheckState  { return nightCheckState(observation.HealthHealthy) }
func nightHealthFailed() nightCheckState   { return nightCheckState(observation.HealthFailed) }
func nightHealthUnknown() nightCheckState  { return nightCheckState(observation.HealthUnknown) }
func nightHealthDegraded() nightCheckState { return nightCheckState(observation.HealthDegraded) }

func nightParseCueOffsets(cues []config.NightSessionCue) []int {
	out := make([]int, 0, len(cues))
	for _, c := range cues {
		out = append(out, c.OffsetMs)
	}
	return out
}
