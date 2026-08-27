// Package currentrun owns the runner-neutral current-runs projection.
//
// It deliberately knows neither HTTP nor a particular runner implementation.
// Readers translate store and collector state into these types, and the API
// serializes the resulting snapshot. This keeps FPP optional and permits the
// FPP and showmesh-audio runners to appear together in one response.
package currentrun

import (
	"context"
	"sort"
	"time"
)

const (
	RunnerFPP           = "fpp"
	RunnerShowmeshAudio = "showmesh-audio"
)

// Evidence is one source-backed reading used by a run. A nil ObservedAt is
// intentional: the reader must not turn receipt time into evidence time.
type Evidence struct {
	Signal      string
	Value       any
	Unit        string
	State       string
	Reason      string
	ObservedAt  *time.Time
	CollectedAt *time.Time
	Source      string
	Quality     string
	ValidFor    time.Duration
}

type ActiveContext struct {
	Configured bool
	Show       string
	Generation int64
}

type Activation struct {
	Show       string
	Generation int64
	PlaylistID string
	Revision   int64
	Runner     string
}

type Playback struct {
	State      string
	Reason     string
	ItemID     string
	ItemIndex  *int
	PositionMs *int64
	Media      string
	Evidence   []Evidence
}

type Freshness struct {
	State       string
	ObservedAt  *time.Time
	CollectedAt *time.Time
	Reason      string
}

type Reconciliation struct {
	State  string
	Reason string
}

type Target struct {
	Kind     string
	ID       string
	Evidence []Evidence
}

// Next is present only when a runner supplied an authoritative next item.
// Readers must leave it nil when next is merely inferable from local config.
type Next struct {
	ItemID    string
	ItemIndex int
	Media     string
	Source    string
}

type Run struct {
	ID               string
	Runner           string
	Show             string
	Generation       int64
	PlaylistID       string
	PlaylistRevision int64
	Status           string
	StatusReason     string
	Playback         Playback
	Freshness        Freshness
	Reconciliation   Reconciliation
	Activation       Activation
	Targets          []Target
	Next             *Next
}

type Snapshot struct {
	Active ActiveContext
	Runs   []Run
}

// ReadFunc is the boundary between this package and coordinator-owned
// stores/collectors. It must return a complete read snapshot for one request.
type ReadFunc func(context.Context, time.Time) (Snapshot, error)

// Coordinator exposes one deterministic current-runs read operation.
type Coordinator struct{ Read ReadFunc }

func (c Coordinator) Snapshot(ctx context.Context, now time.Time) (Snapshot, error) {
	if c.Read == nil {
		return Snapshot{Runs: []Run{}}, nil
	}
	s, err := c.Read(ctx, now)
	if err != nil {
		return Snapshot{}, err
	}
	if s.Runs == nil {
		s.Runs = []Run{}
	}
	for i := range s.Runs {
		r := &s.Runs[i]
		if r.Targets == nil {
			r.Targets = []Target{}
		}
		if r.Playback.Evidence == nil {
			r.Playback.Evidence = []Evidence{}
		}
		for j := range r.Targets {
			if r.Targets[j].Evidence == nil {
				r.Targets[j].Evidence = []Evidence{}
			}
		}
	}
	sort.SliceStable(s.Runs, func(i, j int) bool {
		if s.Runs[i].Runner != s.Runs[j].Runner {
			return s.Runs[i].Runner < s.Runs[j].Runner
		}
		return s.Runs[i].ID < s.Runs[j].ID
	})
	return s, nil
}
