package macro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestGetMacroRunsFiltersFindOlderRunsThroughHTTP seeds one older run of a
// target show, finished, beneath more than a page of newer running runs of
// another show, then asks the real handler for it by show and by state.
func TestGetMacroRunsFiltersFindOlderRunsThroughHTTP(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, svc, _ := newTestStoreAndIdentity(t, func() time.Time { return clock })
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, &fakeBrokers{})
	ctx := context.Background()

	for i := 0; i < store.MaxMacroRunPageSize+2; i++ {
		id := fmt.Sprintf("run-%03d", i)
		show := "other"
		if i == 0 {
			show = "target"
		}
		run := store.MacroRunRecord{
			ID: id, MacroObjectID: "macro-" + id, MacroRevision: 1, Show: show, Trigger: "api",
			IssuerPrincipalID: "p1", IssuerPrincipalName: "tester", IdempotencyKey: "idem-" + id, State: "running",
		}
		steps := []store.MacroRunStepRecord{{
			StepIndex: 0, StepID: "s1", ActionObjectID: "a1", ActionRevision: 1, Integration: "fpp",
			SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "pending",
			OutcomeState: store.MacroRunStepOutcomeStatePending, OutcomeReason: store.MacroRunStepOutcomeReasonPending,
		}}
		if _, _, err := st.CreateMacroRun(ctx, run, steps); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if i == 0 {
			if err := st.FinishMacroRun(ctx, id, store.MacroRunFinishUpdate{FinishedAt: clock}); err != nil {
				t.Fatalf("finish %s: %v", id, err)
			}
		}
		clock = clock.Add(time.Second)
	}

	operator, err := svc.CreatePrincipal(ctx, "operator-1", identity.KindHuman, identity.RoleOperator, "not-a-real-secret-01")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, operator.ID, "test", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	handler := api.New(api.Dependencies{Identity: svc, Commands: st, Macros: e}, api.Options{Logger: testLogger()}).Handler

	for _, query := range []string{"show=target&limit=5", "state=finished&limit=5"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/macro-runs?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+tok.Value)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body: %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode: %v", query, err)
		}
		if len(body.Runs) != 1 {
			t.Fatalf("%s: runs = %d, want the 1 older matching run; body: %s", query, len(body.Runs), rec.Body.String())
		}
	}
}
