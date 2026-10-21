package assetsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Night session states in which no night is open (the api package's own
// nightStateInactive and nightStateStopped).
const (
	nightSessionStateInactive = "inactive"
	nightSessionStateStopped  = "stopped"
)

// NightSessionOpen reports whether a night session exists that has not
// stopped. Sequence testing (ADR-067) is off while one does.
func NightSessionOpen(ctx context.Context, st *store.Store) (bool, error) {
	rec, ok, err := st.GetCurrentNightSession(ctx)
	if err != nil {
		return false, fmt.Errorf("get current night session: %w", err)
	}
	return ok && rec.State != nightSessionStateInactive && rec.State != nightSessionStateStopped, nil
}

// SequenceTestingEnabled reports whether showID's test mappings run now.
func SequenceTestingEnabled(ctx context.Context, st *store.Store) (bool, error) {
	open, err := NightSessionOpen(ctx, st)
	return !open, err
}

// SequenceTestMappings returns showID's stored test mappings as filename ->
// Cue, empty when none are saved. It does not check the night gate.
func SequenceTestMappings(ctx context.Context, st *store.Store, showID string) (map[string]string, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowSequenceTestConfigKind, showID)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get show.sequencetest %q: %w", showID, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowSequenceTestConfigKind, showID, obj.CurrentRevision)
	if err != nil {
		return nil, fmt.Errorf("read show.sequencetest %q revision %d: %w", showID, obj.CurrentRevision, err)
	}
	var p config.SequenceTestPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &p); err != nil {
		return nil, fmt.Errorf("decode show.sequencetest %q: %w", showID, err)
	}
	out := make(map[string]string, len(p.Entries))
	for _, e := range p.Entries {
		out[e.SequenceFilename] = e.Cue
	}
	return out, nil
}

// activeSequenceTestTriggers returns Cue -> mapped filenames for showID, or
// nil while a night session is open.
func activeSequenceTestTriggers(ctx context.Context, st *store.Store, showID string) (map[string][]string, error) {
	enabled, err := SequenceTestingEnabled(ctx, st)
	if err != nil || !enabled {
		return nil, err
	}
	mappings, err := SequenceTestMappings(ctx, st, showID)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(mappings))
	for filename, cue := range mappings {
		out[cue] = append(out[cue], filename)
	}
	return out, nil
}

// PlaylistSequenceFilenameClaims returns filename -> the Cues showID's FPP
// playlist entries play for it, the claims a test mapping may not contradict.
func PlaylistSequenceFilenameClaims(ctx context.Context, st *store.Store, showID string) (map[string][]string, error) {
	seeds, err := cueSequenceFilenameTriggerSeeds(ctx, st, showID)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for cue, filenames := range seeds {
		for _, f := range filenames {
			out[f] = append(out[f], cue)
		}
	}
	return out, nil
}
