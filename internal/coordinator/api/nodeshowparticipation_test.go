package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file covers node show participation (nodeshowparticipation.go): a
// node that participates, a node that does not, and the property that a
// coordinator unable to determine participation must never report
// "not_participating". See that file's own doc comment for why unknown
// and not_configured are kept apart.

// TestNodeShowParticipation_ParticipatingReachesRealAPI reuses
// nightcatalogreadiness_test.go's own fixture: render-01 has a real,
// non-empty resolved catalog for the active show halloween-2026.
func TestNodeShowParticipation_ParticipatingReachesRealAPI(t *testing.T) {
	api, _, token := newNightCatalogReadinessFixture(t, nil)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	node := m["node"].(map[string]any)
	participation := node["showParticipation"].(map[string]any)
	if participation["state"] != "participating" {
		t.Fatalf("state = %v, want participating; participation: %+v", participation["state"], participation)
	}
	if participation["show"] != "halloween-2026" {
		t.Fatalf("show = %v, want halloween-2026", participation["show"])
	}
}

// TestNodeShowParticipation_NotParticipatingReachesRealAPI declares a
// second node in the same fixture with no surface or cue assignment at
// all: the active show's catalog resolves for it, with no output,
// exactly [assetsync.Catalog.HasAnyOutput]'s own false case.
func TestNodeShowParticipation_NotParticipatingReachesRealAPI(t *testing.T) {
	api, st, token := newNightCatalogReadinessFixture(t, nil)
	mustDeclareNode(t, st, "unused-01")

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/unused-01", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	node := m["node"].(map[string]any)
	participation := node["showParticipation"].(map[string]any)
	if participation["state"] != "not_participating" {
		t.Fatalf("state = %v, want not_participating; participation: %+v", participation["state"], participation)
	}
	if participation["show"] != "halloween-2026" {
		t.Fatalf("show = %v, want halloween-2026", participation["show"])
	}
}

// TestNodeShowParticipationNeverReportsNotParticipatingWhenUndetermined
// names the property directly: a coordinator that cannot determine
// participation must never render "not_participating", since that would
// tell an operator an item is safe to ignore on the one night it is not.
// It covers both undeterminable paths: an activeErr, exactly the value a
// genuine transient ResolveActiveShow failure would produce, and a real
// ResolveCueCatalog failure driven by a show.cue object this package's own
// decoder cannot read, the more likely failure in practice since a
// per-node catalog resolution has strictly more ways to fail than a single
// active-show read. Both must turn into "unknown", never fall through to
// the catalog check that could answer "not_participating".
func TestNodeShowParticipationNeverReportsNotParticipatingWhenUndetermined(t *testing.T) {
	t.Run("active show cannot be resolved", func(t *testing.T) {
		_, st, _ := newNightCatalogReadinessFixture(t, nil)
		got := nodeShowParticipation(context.Background(), st, assetsync.ActiveShow{}, errors.New("injected: store unavailable"), "render-01")
		assertNeverNotParticipating(t, got)
	})

	t.Run("cue catalog cannot be resolved", func(t *testing.T) {
		_, st, _ := newNightCatalogReadinessFixture(t, nil)
		corruptShowCueRevision(t, st, "thriller")

		active, err := assetsync.ResolveActiveShow(context.Background(), st)
		if err != nil || !active.Configured {
			t.Fatalf("resolve active show: configured=%v err=%v", active.Configured, err)
		}
		got := nodeShowParticipation(context.Background(), st, active, nil, "render-01")
		assertNeverNotParticipating(t, got)
		if got.Show != "halloween-2026" {
			t.Fatalf("show = %q, want halloween-2026 even though the catalog itself could not be resolved", got.Show)
		}
	})
}

// corruptShowCueRevision writes a new, unvalidated revision of an
// existing show.cue object directly to the store, bypassing the PUT
// handler's own config.DecodeShowCuePayload check, and activates it as
// current. This is the one way this package can drive a REAL
// ResolveCueCatalog failure: its own decode of the stored payload
// (cuecatalog.go) fails exactly the way a hand-corrupted store row would.
func corruptShowCueRevision(t *testing.T, st *store.Store, cueID string) {
	t.Helper()
	ctx := context.Background()
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if err != nil {
		t.Fatalf("get show.cue %q: %v", cueID, err)
	}
	next := obj.CurrentRevision + 1
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: cueID, Revision: next,
		PayloadJSON: "not valid json", Source: "env_migration", Note: "test-injected corruption",
	}); err != nil {
		t.Fatalf("create corrupt config revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, cueID, next); err != nil {
		t.Fatalf("activate corrupt config revision: %v", err)
	}
}

func assertNeverNotParticipating(t *testing.T, got v1.NodeShowParticipation) {
	t.Helper()
	if got.State == "not_participating" {
		t.Fatalf("state = not_participating for an undetermined result; must be unknown. got: %+v", got)
	}
	if got.State != "unknown" {
		t.Fatalf("state = %q, want unknown", got.State)
	}
	if got.Reason == nil || *got.Reason == "" {
		t.Fatalf("reason must be populated for unknown, got: %+v", got)
	}
}
