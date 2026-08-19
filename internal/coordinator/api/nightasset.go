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
		return nightAssetFSEQDurationResult{Reason: "failed to look up the pinned asset: " + err.Error()}
	}
	if !ok {
		return nightAssetFSEQDurationResult{Reason: fmt.Sprintf("no current asset for show %q sequence %q target %q", show, ref.Sequence, ref.Target)}
	}
	if rec.MediaType != "fseq" {
		return nightAssetFSEQDurationResult{Reason: fmt.Sprintf("the pinned asset's media type is %q, not \"fseq\"", rec.MediaType)}
	}

	f, cleanup, err := nightOpenFSEQAsset(ctx, deps, rec)
	if err != nil {
		return nightAssetFSEQDurationResult{Reason: "failed to open the pinned FSEQ asset: " + err.Error()}
	}
	defer cleanup()

	frameCount := f.FrameCount()
	stepTimeMS := f.StepTimeMS()
	switch {
	case frameCount == 0 && stepTimeMS == 0:
		return nightAssetFSEQDurationResult{Reason: "the FSEQ has both a zero frame count and a zero step time; it carries no usable duration", Filename: rec.RuntimeFilename}
	case frameCount == 0:
		return nightAssetFSEQDurationResult{Reason: "the FSEQ has a zero frame count (an empty sequence)", Filename: rec.RuntimeFilename}
	case stepTimeMS == 0:
		return nightAssetFSEQDurationResult{Reason: "the FSEQ has a zero step time (unset frame rate)", Filename: rec.RuntimeFilename}
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
func nightCheckRestingPlaylistShape(ctx context.Context, endpoint, playlistName string) nightReadinessCheck {
	name := "resting:playlist-shape:" + playlistName
	def, err := nightReadPlaylistDefinition(ctx, endpoint, playlistName)
	if err != nil {
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: "could not read the resting playlist definition: " + err.Error()}
	}
	if len(def.MainPlaylist) != 1 {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("expected exactly one mainPlaylist item, found %d", len(def.MainPlaylist))}
	}
	item := def.MainPlaylist[0]
	if item.Type != "sequence" || item.MediaName != "" {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf("the resting playlist's item carries an FPP audio association (type %q, mediaName %q); the resting playlist must be FSEQ-only", item.Type, item.MediaName)}
	}
	if item.SequenceName == "" {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: "the resting playlist's item names no sequence"}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(), reason: "exactly one FSEQ-only item, naming a sequence"}
}

// nightCheckRestingAssetExactVariant reports whether the exact deployed
// FSEQ variant is confirmed running — always not_verifiable: FPP's
// playlist read exposes only a filename, never a content hash, so this
// can never be confirmed by this build. Kept separate from the shape
// check rather than folded into its reason string.
func nightCheckRestingAssetExactVariant(playlistName string) nightReadinessCheck {
	return nightReadinessCheck{
		name: "resting:asset-exact-variant:" + playlistName, health: nightCheckStateNotVerifiable,
		reason: "FPP's playlist read exposes only a filename, never a content hash; this coordinator cannot independently confirm the live host is running the pinned asset's exact bytes, and no configuration of this build can make it able to",
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
		return nightReadinessCheck{name: name, health: nightHealthUnknown(), reason: "could not read the show playlist definition: " + err.Error()}
	}
	if len(def.MainPlaylist) == 0 {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: "the show playlist has no items"}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(),
		reason: "playlist exists and is non-empty; whether it is unexpectedly busy at start-night time is enforced at dispatch (startPlaylist's own ifBusy=refuse guard), not by this readiness snapshot"}
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
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: res.Reason}
	}
	for _, off := range cueOffsetsMs {
		if off < 0 && int64(-off) > res.DurationMS {
			return nightReadinessCheck{name: name, health: nightHealthFailed(),
				reason: fmt.Sprintf("a configured cue offset of %dms starts before the resting timeline begins (duration %dms)", off, res.DurationMS)}
		}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy(),
		reason: fmt.Sprintf("resolved duration %dms from %q", res.DurationMS, res.Filename)}
}

func nightHealthHealthy() nightCheckState { return nightCheckState(observation.HealthHealthy) }
func nightHealthFailed() nightCheckState  { return nightCheckState(observation.HealthFailed) }
func nightHealthUnknown() nightCheckState { return nightCheckState(observation.HealthUnknown) }

func nightParseCueOffsets(cues []config.NightSessionCue) []int {
	out := make([]int, 0, len(cues))
	for _, c := range cues {
		out = append(out, c.OffsetMs)
	}
	return out
}
