package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// NewCurrentRunsReader builds the production read adapter. It reads all
// runner families through the existing consumer-side dependencies and leaves
// activation/reconciliation decisions in this server-side projection.
func NewCurrentRunsReader(deps Dependencies) CurrentRunsReader {
	reader := currentRunsReader{deps: deps.withDefaults()}
	return currentrun.Coordinator{Read: reader.Snapshot}
}

type currentRunsReader struct{ deps Dependencies }

func (r currentRunsReader) Snapshot(ctx context.Context, now time.Time) (currentrun.Snapshot, error) {
	active, err := readCurrentActive(ctx, r.deps.Config)
	if err != nil {
		return currentrun.Snapshot{}, err
	}
	playlists, err := readCurrentPlaylists(ctx, r.deps.Config)
	if err != nil {
		return currentrun.Snapshot{}, err
	}
	inputs := currentrun.Snapshot{Active: active, Runs: []currentrun.Run{}}
	if err := r.appendFPPRuns(ctx, now, &inputs, playlists); err != nil {
		return currentrun.Snapshot{}, err
	}
	if err := r.appendAudioRuns(ctx, now, &inputs, playlists); err != nil {
		return currentrun.Snapshot{}, err
	}
	return inputs, nil
}

func readCurrentActive(ctx context.Context, cfg ConfigStore) (currentrun.ActiveContext, error) {
	obj, err := cfg.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return currentrun.ActiveContext{}, nil
	}
	if err != nil {
		return currentrun.ActiveContext{}, fmt.Errorf("read current active show: %w", err)
	}
	rev, err := cfg.GetConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, obj.CurrentRevision)
	if err != nil {
		return currentrun.ActiveContext{}, fmt.Errorf("read current active show revision: %w", err)
	}
	var p config.ShowActivePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &p); err != nil {
		return currentrun.ActiveContext{}, fmt.Errorf("decode current active show: %w", err)
	}
	return currentrun.ActiveContext{Configured: true, Show: p.Show, Generation: obj.CurrentRevision}, nil
}

type currentPlaylist struct {
	ID       string
	Revision int64
	Payload  config.ShowPlaylistPayload
}

func readCurrentPlaylists(ctx context.Context, cfg ConfigStore) ([]currentPlaylist, error) {
	objs, err := cfg.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list current show playlists: %w", err)
	}
	out := make([]currentPlaylist, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := cfg.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("read current playlist %q: %w", obj.ID, err)
		}
		var p config.ShowPlaylistPayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &p); err != nil {
			return nil, fmt.Errorf("decode current playlist %q: %w", obj.ID, err)
		}
		out = append(out, currentPlaylist{ID: obj.ID, Revision: obj.CurrentRevision, Payload: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r currentRunsReader) appendFPPRuns(ctx context.Context, now time.Time, out *currentrun.Snapshot, playlists []currentPlaylist) error {
	views, err := r.deps.FPP.ListInstances(ctx)
	if err != nil {
		return fmt.Errorf("read current fpp instances: %w", err)
	}
	byUUID := make(map[string]FPPInstanceView)
	for _, view := range views {
		if view.InstanceUUID != nil {
			byUUID[view.InstanceUUID.UUID] = view
		}
	}
	obs, err := r.deps.FPPObservations.ListFPPPlaylistEntryObservations(ctx)
	if err != nil {
		return fmt.Errorf("read current fpp playback: %w", err)
	}
	for _, rec := range obs {
		view := byUUID[rec.InstanceUUID]
		var reconciliation currentrun.Reconciliation
		result, reconcileErr := r.deps.FPPReconciliation.ReconcileFPPPlaylistEntryObservation(ctx, rec)
		if reconcileErr != nil {
			reconciliation = currentrun.Reconciliation{State: "unknown", Reason: reconcileErr.Error()}
		} else {
			reconciliation = currentrun.Reconciliation{State: string(result.Outcome), Reason: result.Reason}
		}
		pl := findFPPPlaylist(playlists, rec.InstanceUUID, rec.PlaylistHash, rec.PlaylistName)
		show, gen, playlistID, playlistRev := "", int64(0), "", int64(0)
		if pl != nil {
			show, playlistID, playlistRev = pl.Payload.Show, pl.ID, pl.Revision
			if out.Active.Configured && out.Active.Show == show {
				gen = out.Active.Generation
			}
		}
		status, reason := fppRunStatus(rec)
		var idx *int
		if rec.EntryKey != "" {
			i := int(rec.Position)
			idx = &i
		}
		freshState := currentEvidenceState(now, rec.ObservedAt)
		run := currentrun.Run{
			ID: "fpp:" + rec.InstanceUUID, Runner: currentrun.RunnerFPP,
			Show: show, Generation: gen, PlaylistID: playlistID, PlaylistRevision: playlistRev,
			Status: status, StatusReason: reason,
			Playback:       currentrun.Playback{State: status, Reason: reason, ItemID: rec.EntryKey, ItemIndex: idx, Media: rec.MediaFilename},
			Freshness:      currentrun.Freshness{State: freshState, Reason: currentRunFreshnessReason(freshState), ObservedAt: currentRunTimePtr(rec.ObservedAt), CollectedAt: currentRunTimePtr(rec.ReceivedAt)},
			Reconciliation: reconciliation,
			Activation:     currentrun.Activation{Show: show, Generation: gen, PlaylistID: playlistID, Revision: playlistRev, Runner: currentrun.RunnerFPP},
			Targets:        []currentrun.Target{{Kind: string(observation.ResourceFPP), ID: rec.InstanceUUID, Evidence: currentEvidenceList(view.Observations, now)}},
		}
		run.Playback.Evidence = currentEvidenceList(view.Observations, now)
		out.Runs = append(out.Runs, run)
	}
	return nil
}

func findFPPPlaylist(playlists []currentPlaylist, uuid, hash, name string) *currentPlaylist {
	for i := range playlists {
		p := &playlists[i]
		if p.Payload.Runner == config.ShowPlaylistRunnerFPP && p.Payload.FPP != nil && p.Payload.FPP.InstanceUUID == uuid && p.Payload.FPP.PlaylistHash == hash && p.Payload.FPP.PlaylistName == name {
			return p
		}
	}
	return nil
}

func fppRunStatus(rec store.FPPPlaylistEntryObservationRecord) (string, string) {
	if rec.Unavailable != "" {
		return "unavailable", rec.Unavailable
	}
	action := strings.ToLower(rec.Action)
	switch action {
	case "play", "start", "playing":
		return "playing", "FPP reported active playback"
	case "stop", "stopped", "stop_playlist":
		return "stopped", "FPP reported stopped playback"
	default:
		if rec.PlaylistName == "" && rec.MediaFilename == "" {
			return "idle", "FPP reported no current playlist entry"
		}
		return "unknown", "FPP playback action is unavailable"
	}
}

func (r currentRunsReader) appendAudioRuns(ctx context.Context, now time.Time, out *currentrun.Snapshot, playlists []currentPlaylist) error {
	nodes, err := r.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		return fmt.Errorf("read current audio nodes: %w", err)
	}
	var audioPlaylist *currentPlaylist
	if out.Active.Configured {
		for i := range playlists {
			if playlists[i].Payload.Show == out.Active.Show && playlists[i].Payload.Runner == config.ShowPlaylistRunnerShowmeshAudio {
				audioPlaylist = &playlists[i]
				break
			}
		}
	}
	for _, node := range nodes {
		observations := r.deps.Audio.NodeAudioObservations(node.NodeID)
		bySession := make(map[string][]observation.Observation)
		for _, o := range observations {
			if o.Resource.Kind == observation.ResourceAudioSession {
				bySession[o.Resource.ID] = append(bySession[o.Resource.ID], o)
			}
		}
		for sessionID, sessionObs := range bySession {
			run := audioRun(now, out.Active, audioPlaylist, node.NodeID, sessionID, sessionObs)
			out.Runs = append(out.Runs, run)
		}
	}
	return nil
}

func audioRun(now time.Time, active currentrun.ActiveContext, pl *currentPlaylist, nodeID, sessionID string, obs []observation.Observation) currentrun.Run {
	state, reason := "unknown", "no audio session state evidence"
	itemID, media := "", ""
	var itemIndex, position, playlistRevision *int64
	var newest *observation.Observation
	for i := range obs {
		o := obs[i]
		if newest == nil || (o.ObservedAt != nil && (newest.ObservedAt == nil || o.ObservedAt.After(*newest.ObservedAt))) {
			copy := o
			newest = &copy
		}
		switch string(o.Signal) {
		case "audio_session.state":
			state, _ = o.Value.(string)
			reason = audioStateReason(state)
		case "audio_session.playlist.item_id":
			itemID, _ = o.Value.(string)
		case "audio_session.playlist.item_index":
			itemIndex = int64PtrValue(o.Value)
		case "audio_session.position_ms":
			position = int64PtrValue(o.Value)
		case "audio_session.playlist.revision":
			playlistRevision = int64PtrValue(o.Value)
		}
	}
	show, generation, playlistID, revision := "", int64(0), "", int64(0)
	if pl != nil {
		show, playlistID, revision = pl.Payload.Show, pl.ID, pl.Revision
		if active.Configured && active.Show == show {
			generation = active.Generation
		}
	}
	recon := currentrun.Reconciliation{State: "unbound", Reason: "no active showmesh-audio playlist is bound to this session"}
	if pl != nil {
		recon = currentrun.Reconciliation{State: "resolved", Reason: "audio session playlist is resolved against the active Show"}
		if playlistRevision != nil && *playlistRevision != revision {
			recon = currentrun.Reconciliation{State: "stale-import", Reason: "audio session reports a different playlist revision than the active playlist"}
		}
	}
	fresh := currentrun.Freshness{State: "not_collected", Reason: "no audio session evidence"}
	if newest != nil {
		freshState := currentEvidenceState(now, derefTime(newest.ObservedAt))
		fresh = currentrun.Freshness{State: freshState, Reason: currentRunFreshnessReason(freshState), ObservedAt: newest.ObservedAt, CollectedAt: currentRunTimePtr(newest.CollectedAt)}
	}
	run := currentrun.Run{ID: "showmesh-audio:" + nodeID + ":" + sessionID, Runner: currentrun.RunnerShowmeshAudio,
		Show: show, Generation: generation, PlaylistID: playlistID, PlaylistRevision: revision,
		Status: state, StatusReason: reason,
		Playback:  currentrun.Playback{State: state, Reason: reason, ItemID: itemID, PositionMs: position, Media: media, Evidence: currentEvidenceList(obs, now)},
		Freshness: fresh, Reconciliation: recon,
		Activation: currentrun.Activation{Show: show, Generation: generation, PlaylistID: playlistID, Revision: revision, Runner: currentrun.RunnerShowmeshAudio},
		Targets:    []currentrun.Target{{Kind: string(observation.ResourceNode), ID: nodeID, Evidence: currentEvidenceList(obs, now)}},
	}
	if itemIndex != nil {
		i := int(*itemIndex)
		run.Playback.ItemIndex = &i
	}
	return run
}

func audioStateReason(state string) string {
	switch state {
	case "playing":
		return "audio session is playing"
	case "paused":
		return "audio session is paused"
	case "stopped", "idle", "ready":
		return "audio session is not actively playing"
	default:
		return "audio session state is unavailable"
	}
}

func currentEvidenceList(in []observation.Observation, now time.Time) []currentrun.Evidence {
	out := make([]currentrun.Evidence, 0, len(in))
	for _, o := range in {
		state := o.StateAt(now)
		reason := ""
		if state != observation.StateCurrent {
			reason = evidenceReason(o, state, now)
		} else if o.Reason != "" {
			reason = o.Reason
		}
		collectedAt := currentRunTimePtr(o.CollectedAt)
		if state == observation.StateNotCollected {
			collectedAt = nil
		}
		out = append(out, currentrun.Evidence{Signal: string(o.Signal), Value: o.Value, Unit: o.Unit, State: string(state), Reason: reason, ObservedAt: o.ObservedAt, CollectedAt: collectedAt, Source: o.Source, Quality: string(o.Quality), ValidFor: o.ValidFor})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Signal < out[j].Signal })
	return out
}

func currentEvidenceState(now time.Time, at time.Time) string {
	if at.IsZero() {
		return string(observation.StateUnknownAge)
	}
	if now.Sub(at) > 45*time.Second {
		return string(observation.StateStale)
	}
	return string(observation.StateCurrent)
}

func currentRunFreshnessReason(state string) string {
	switch state {
	case string(observation.StateCurrent):
		return "latest runner evidence is fresh"
	case string(observation.StateStale):
		return "latest runner evidence has aged past its freshness window"
	case string(observation.StateUnknownAge):
		return "latest runner evidence has no observation time"
	default:
		return "runner evidence freshness is unavailable"
	}
}

func int64PtrValue(v any) *int64 {
	switch n := v.(type) {
	case int64:
		return &n
	case int:
		x := int64(n)
		return &x
	case float64:
		x := int64(n)
		return &x
	case json.Number:
		x, err := n.Int64()
		if err == nil {
			return &x
		}
	}
	return nil
}

func currentRunTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func mapCurrentEvidence(e currentrun.Evidence) v1.Evidence {
	result := v1.Evidence{Signal: e.Signal, Value: e.Value, Unit: stringPtrOrNil(e.Unit), State: e.State, Reason: stringPtrOrNil(e.Reason), ObservedAt: formatTimePtr(e.ObservedAt), CollectedAt: formatTimePtr(e.CollectedAt), Source: e.Source, Quality: e.Quality}
	if e.ValidFor > 0 {
		seconds := int64(e.ValidFor / time.Second)
		result.ValidForSeconds = &seconds
	}
	return result
}

func mapCurrentRun(r currentrun.Run) v1.CurrentRun {
	targets := make([]v1.CurrentRunTarget, 0, len(r.Targets))
	for _, t := range r.Targets {
		e := make([]v1.Evidence, 0, len(t.Evidence))
		for _, v := range t.Evidence {
			e = append(e, mapCurrentEvidence(v))
		}
		targets = append(targets, v1.CurrentRunTarget{Kind: t.Kind, ID: t.ID, Evidence: e})
	}
	pe := make([]v1.Evidence, 0, len(r.Playback.Evidence))
	for _, e := range r.Playback.Evidence {
		pe = append(pe, mapCurrentEvidence(e))
	}
	var next *v1.CurrentRunNext
	if r.Next != nil {
		next = &v1.CurrentRunNext{ItemID: r.Next.ItemID, ItemIndex: r.Next.ItemIndex, Media: r.Next.Media, Source: r.Next.Source}
	}
	return v1.CurrentRun{ID: r.ID, Runner: r.Runner, Show: r.Show, Generation: r.Generation, PlaylistID: r.PlaylistID, PlaylistRevision: r.PlaylistRevision, Status: r.Status, StatusReason: r.StatusReason,
		Playback:       v1.CurrentPlayback{State: r.Playback.State, Reason: r.Playback.Reason, ItemID: r.Playback.ItemID, ItemIndex: r.Playback.ItemIndex, PositionMs: r.Playback.PositionMs, Media: r.Playback.Media, Evidence: pe},
		Freshness:      v1.CurrentRunFreshness{State: r.Freshness.State, Reason: r.Freshness.Reason, ObservedAt: formatTimePtr(r.Freshness.ObservedAt), CollectedAt: formatTimePtr(r.Freshness.CollectedAt)},
		Reconciliation: v1.CurrentReconciliation{State: r.Reconciliation.State, Reason: r.Reconciliation.Reason},
		Activation:     v1.CurrentRunActivation{Show: r.Activation.Show, Generation: r.Activation.Generation, PlaylistID: r.Activation.PlaylistID, Revision: r.Activation.Revision, Runner: r.Activation.Runner}, Targets: targets, Next: next}
}

func mapCurrentSnapshot(s currentrun.Snapshot, now time.Time) v1.CurrentRunsResponse {
	var show *string
	var generation *int64
	if s.Active.Configured {
		show = &s.Active.Show
		generation = &s.Active.Generation
	}
	runs := make([]v1.CurrentRun, 0, len(s.Runs))
	for _, r := range s.Runs {
		runs = append(runs, mapCurrentRun(r))
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].Runner != runs[j].Runner {
			return runs[i].Runner < runs[j].Runner
		}
		return runs[i].ID < runs[j].ID
	})
	return v1.CurrentRunsResponse{ServerTime: formatTime(now), ActiveShow: v1.CurrentShowContext{Configured: s.Active.Configured, Show: show, Generation: generation}, Runs: runs}
}

// currentRunsDiffProjection masks receipt and evidence timestamps only for
// stream change detection. Current state, values, reasons, and transitions
// remain visible, while a polling collector cannot turn an unchanged run
// into a stream event merely by restamping a successful read.
func currentRunsDiffProjection(in v1.CurrentRunsResponse) v1.CurrentRunsResponse {
	out := in
	out.ServerTime = ""
	out.Runs = make([]v1.CurrentRun, len(in.Runs))
	for i, run := range in.Runs {
		out.Runs[i] = run
		out.Runs[i].Freshness.ObservedAt = nil
		out.Runs[i].Freshness.CollectedAt = nil
		out.Runs[i].Playback.Evidence = make([]v1.Evidence, len(run.Playback.Evidence))
		for j, ev := range run.Playback.Evidence {
			out.Runs[i].Playback.Evidence[j] = ev
			out.Runs[i].Playback.Evidence[j].ObservedAt = nil
			out.Runs[i].Playback.Evidence[j].CollectedAt = nil
			out.Runs[i].Playback.Evidence[j].Source = ""
		}
		out.Runs[i].Targets = make([]v1.CurrentRunTarget, len(run.Targets))
		for j, target := range run.Targets {
			out.Runs[i].Targets[j] = target
			out.Runs[i].Targets[j].Evidence = make([]v1.Evidence, len(target.Evidence))
			for k, ev := range target.Evidence {
				out.Runs[i].Targets[j].Evidence[k] = ev
				out.Runs[i].Targets[j].Evidence[k].ObservedAt = nil
				out.Runs[i].Targets[j].Evidence[k].CollectedAt = nil
				out.Runs[i].Targets[j].Evidence[k].Source = ""
			}
		}
	}
	return out
}

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *handlers) handleCurrentRuns(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	s, err := h.deps.CurrentRuns.Snapshot(r.Context(), now)
	if err != nil {
		h.writeInternalError(w, now, "read current runs", err)
		return
	}
	jsonWrite(w, mapCurrentSnapshot(s, now))
}
