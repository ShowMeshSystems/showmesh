package coordinator

// This file is review finding 5's own fix: the acceptance criterion 7 half
// this package's own macro-package tests could not reach, because a fake
// resolumeActionDispatcher can only assert what the macro package itself
// wrote, never that a run genuinely re-resolves against the composition
// Dispatch reads at the instant it runs. This file proves that property
// through the real chain: a real *resolume.Collector, a real
// *resolume.ActionDispatcher, the real resolumeActionDispatcherAdapter this
// package's own resolumeactionwiring.go declares, and a real
// *macro.Executor — the identical stack
// resolumeactionwiring_e2e_test.go already proves for a single dispatched
// action, extended here across two composition uploads and one macro run.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/macro"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// e2eMacroTestIssuer mirrors internal/coordinator/macro's own testIssuer():
// this package cannot import that package's _test.go helpers, and a run
// submitted directly through *macro.Executor (bypassing HTTP auth
// entirely, exactly as this file's macro run is) needs no principal to
// actually exist for identity.Service.WriteAudit to record one.
func e2eMacroTestIssuer() api.FPPCommandIssuer {
	return api.FPPCommandIssuer{PrincipalID: "e2e-operator", PrincipalName: "e2e-operator", Form: identity.FormToken, CredentialID: "e2e-cred"}
}

// putE2EShowAction writes a show.action config revision directly, mirroring
// internal/coordinator/macro's own putAction test helper (that package's
// own _test.go, not importable here).
func putE2EShowAction(t *testing.T, st *store.Store, id string, payload config.ShowActionPayload) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, config.ShowActionConfigKind, id); err != nil {
		t.Fatalf("create action object %q: %v", id, err)
	}
	payloadJSON, err := config.EncodeShowActionPayload(payload)
	if err != nil {
		t.Fatalf("encode action payload %q: %v", id, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActionConfigKind, ObjectID: id, Revision: 1, PayloadJSON: payloadJSON,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create action revision %q: %v", id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActionConfigKind, id, 1); err != nil {
		t.Fatalf("activate action revision %q: %v", id, err)
	}
}

// putE2EShowMacro is putE2EShowAction's show.macro equivalent.
func putE2EShowMacro(t *testing.T, st *store.Store, id string, payload config.ShowMacroPayload) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, config.ShowMacroConfigKind, id); err != nil {
		t.Fatalf("create macro object %q: %v", id, err)
	}
	payloadJSON, err := config.EncodeShowMacroPayload(payload)
	if err != nil {
		t.Fatalf("encode macro payload %q: %v", id, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowMacroConfigKind, ObjectID: id, Revision: 1, PayloadJSON: payloadJSON,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create macro revision %q: %v", id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowMacroConfigKind, id, 1); err != nil {
		t.Fatalf("activate macro revision %q: %v", id, err)
	}
}

// TestMacroRunResolumeReRunsResolutionAgainstTheCurrentComposition is
// acceptance criterion 7's run-time-re-resolution half: a launchClip
// action authored (and accepted) while the composition contains the named
// clip is refused, at RUN time, naming the clip, once a real re-upload
// changes the composition out from under it — proven through the real
// resolume.ActionDispatcher/resolumeActionDispatcherAdapter chain, never a
// fake standing in for the property under test.
func TestMacroRunResolumeReRunsResolutionAgainstTheCurrentComposition(t *testing.T) {
	cases := []struct {
		name       string
		id         string
		revision2  *resolumecomp.Composition
		wantReason string
	}{
		{
			// The clip survives under a different name: the SAME
			// underlying object (ADR-037 decision 6's own "a rename is
			// the operator saying this is a different thing"), reachable
			// under the old name no longer.
			name: "clip renamed", id: "clip-renamed",
			revision2: &resolumecomp.Composition{
				Name:      "D-3 wiring E2E fixture v2",
				WrittenBy: resolumecomp.WrittenBy{Product: "Resolume Arena", Major: 7, Minor: 23, Micro: 2, Revision: 1},
				Canvas:    resolumecomp.Canvas{Width: 1920, Height: 1080},
				Decks:     []resolumecomp.Deck{{ID: e2eDeckID, Name: "Deck One"}},
				Layers:    []resolumecomp.Layer{{ID: e2eLayerID, Index: 0}},
				Clips:     []resolumecomp.Clip{{ID: e2eClipID, DeckID: e2eDeckID, LayerIndex: 0, Name: "E2E Clip Renamed"}},
			},
			wantReason: "E2E Clip",
		},
		{
			// The clip is gone entirely: an otherwise-valid composition
			// with no clip of that name anywhere, distinct from a rename.
			name: "clip removed", id: "clip-removed",
			revision2: &resolumecomp.Composition{
				Name:      "D-3 wiring E2E fixture v3",
				WrittenBy: resolumecomp.WrittenBy{Product: "Resolume Arena", Major: 7, Minor: 23, Micro: 2, Revision: 1},
				Canvas:    resolumecomp.Canvas{Width: 1920, Height: 1080},
				Decks:     []resolumecomp.Deck{{ID: e2eDeckID, Name: "Deck One"}},
				Layers:    []resolumecomp.Layer{{ID: e2eLayerID, Index: 0}},
				Clips:     nil,
			},
			wantReason: "E2E Clip",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arena := newE2EFakeArena()
			srv := httptest.NewServer(arena)
			defer srv.Close()

			ctx := context.Background()
			st := openTestStore(t)
			logger := testLogger()

			// Revision 1: the composition the action is authored against.
			writeTestCompositionRevision(t, st, 1, e2eTestComposition())
			compWiring := newResolumeCompositionWiring(ctx, st, logger)
			if rev := compWiring.store.LoadedRevision(); rev != 1 {
				t.Fatalf("composition revision loaded at startup = %d, want 1", rev)
			}

			cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-e2e-macro-" + tc.id}
			sink := &fppSink{st: st, logger: logger}
			runner := collector.NewRunner(sink, logger)

			wire, err := newResolumeWiring(ctx, cfg, runner, compWiring.store, logger)
			if err != nil {
				t.Fatalf("newResolumeWiring: %v", err)
			}
			if _, ok := wire.collector.Poll(ctx); !ok {
				t.Fatal("wire.collector.Poll: did not run")
			}

			// Author the action while revision 1 (containing "E2E Clip")
			// is current — the write-time half of criterion 1/2, through
			// the real config.ResolumeReferenceResolver adapter, not a
			// fake standing in for it.
			resolver := newResolumeReferenceResolverAdapter(compWiring.store)
			actionJSON := `{"show":"e2e-show","label":"Launch E2E clip","safetyClass":"none",
				"target":{"integration":"resolume","action":"launchClip","ref":{"clip":"E2E Clip","deck":"Deck One"}}}`
			payload, verr := config.DecodeShowActionPayload(actionJSON, nil, nil, nil, resolver)
			if verr != nil {
				t.Fatalf("action authoring was refused while the clip still resolved: %+v", verr)
			}
			putE2EShowAction(t, st, "launch-e2e-clip", payload)
			putE2EShowMacro(t, st, "m1", config.ShowMacroPayload{
				Show: "e2e-show", Label: "E2E macro",
				Steps: []config.ShowMacroStep{{
					ID: "s1", Action: "launch-e2e-clip",
					OnFailure: config.ShowMacroOnFailureContinue, OnUnconfirmed: config.ShowMacroOnUnconfirmedContinue,
					LocalFallback: config.ShowMacroLocalFallback{Class: config.ShowMacroLocalFallbackCoordinatorRequired, Reason: "resolume is coordinator-hosted"},
				}},
			})

			// Re-upload: the composition genuinely changes, and the
			// periodic refresh (compWiring.Run's own ticker in
			// production) picks it up — called directly here rather than
			// waiting out resolumeCompositionRefreshInterval.
			writeTestCompositionRevision(t, st, 2, tc.revision2)
			if err := compWiring.store.Refresh(ctx, compWiring.reader); err != nil {
				t.Fatalf("refresh composition store to revision 2: %v", err)
			}
			if rev := compWiring.store.LoadedRevision(); rev != 2 {
				t.Fatalf("composition revision after refresh = %d, want 2", rev)
			}

			svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(logger))
			adapter := newResolumeActionDispatcherAdapter(wire.collector)
			exec := macro.NewExecutor(macro.Dependencies{
				Store: st, Identity: svc, ResolumeActions: adapter, Clock: time.Now, Logger: logger,
			}, macro.Options{})

			result, problem, err := exec.SubmitRun(ctx, api.MacroSubmitRequest{
				MacroObjectID: "m1", IdempotencyKey: "e2e-run-" + tc.name, Trigger: "api", Issuer: e2eMacroTestIssuer(),
			})
			if err != nil || problem != nil {
				t.Fatalf("submit run: problem=%+v err=%v", problem, err)
			}
			// Blocks until the run's own background goroutine (SubmitRun's
			// "go e.executeRun(...)") has finished, exactly as
			// Executor.Stop's own doc comment describes — the exported
			// equivalent of the macro package's own internal e.wg.Wait(),
			// which this package cannot reach directly.
			if err := exec.Stop(context.Background()); err != nil {
				t.Fatalf("wait for run to finish: %v", err)
			}

			got, err := exec.GetRun(ctx, result.Run.ID)
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			if len(got.Steps) != 1 {
				t.Fatalf("unexpected step count: %+v", got.Steps)
			}
			step := got.Steps[0]
			if step.Outcome != "failed" {
				t.Fatalf("step outcome = %q, want %q (a stale reference must refuse at run time): %+v", step.Outcome, "failed", step)
			}
			if !strings.Contains(step.OutcomeReason, tc.wantReason) {
				t.Fatalf("step OutcomeReason = %q, want it to name %q", step.OutcomeReason, tc.wantReason)
			}

			// The proof this test exists for: resolution happened AGAIN,
			// against the NEW composition, not against whatever
			// DecodeShowActionPayload accepted at write time. If the run
			// path trusted the write-time result instead, this dispatch
			// would have gone to Arena for the old, now-stale object id.
			wantPath := "POST /api/v1/composition/clips/by-id/" + e2eClipID + "/connect"
			for _, r := range arena.requestLog() {
				if r == wantPath {
					t.Fatalf("arena received a dispatch for the stale reference (%s); the run must refuse before ever reaching Arena", wantPath)
				}
			}
		})
	}
}
