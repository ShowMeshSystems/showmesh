package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Track B seam B2b-front's own OpenAPI conformance coverage,
// following openapi_resolumeaction_test.go's split exactly: a compile-only
// pass over every schema this seam added (so a malformed schema fails
// CI even if no test happens to exercise every branch), and a real-handler
// pass driving a REAL [API] built from renderdispatch_test.go's own real
// store.Store/identity.Service fixtures — never a hand-built JSON fixture
// standing in for a real response.

// TestOpenAPIRenderDispatchSchemasCompile proves RenderApplyRequest,
// RenderSurfaceRequest, RenderCommandResponse, and RenderCommandResult
// are all well-formed and reachable from api/openapi.yaml's components,
// independent of any one test happening to produce a matching response.
func TestOpenAPIRenderDispatchSchemasCompile(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"RenderApplyRequest", "RenderSurfaceRequest", "RenderCommandResponse", "RenderCommandResult",
	} {
		if _, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name); err != nil {
			t.Errorf("compiling schema %s: %v", name, err)
		}
	}
}

// TestOpenAPIRenderApplyResponseMatchesRealResponse drives a real
// render.surface.apply dispatch (resolvable asset, evidence arriving
// asynchronously) through a real *API and validates the real 200 body
// against RenderCommandResponse.
func TestOpenAPIRenderApplyResponseMatchesRealResponse(t *testing.T) {
	renderCommandConfirmDeadline = 2 * time.Second
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	c := newOpenAPICompiler(t)
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	renderPutShow(t, setup.st, "halloween-2026", "Halloween 2026")
	renderPutActiveShow(t, setup.st, "halloween-2026")
	renderPutSurface(t, setup.st, "wall-1", "halloween-2026", "media-01")
	renderCreateAsset(t, setup.st, "halloween-2026", "opener", store.AssetTargetKindNode, "media-01", "hash-a", "opener.fseq")

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	go func() {
		time.Sleep(50 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(time.Second), testNow.Add(time.Second))})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "RenderCommandResponse", body)
}

// TestOpenAPIRenderClearUnconfirmedResponseMatchesRealResponse proves the
// OTHER outcome branch (unconfirmed) also matches the schema — outcome
// enums and the nullable resolvedAt/outcomeState/outcomeReason fields
// specifically.
func TestOpenAPIRenderClearUnconfirmedResponseMatchesRealResponse(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	c := newOpenAPICompiler(t)
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", `{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "RenderCommandResponse", body)

	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Errorf("outcome = %v, want unconfirmed", cmd["outcome"])
	}
}

// TestOpenAPIRenderTransportProbeResponseMatchesRealResponse proves
// dispatchRenderTransportProbe's response (Track B seam B4) matches the
// same RenderCommandResponse schema its sibling endpoints use — it
// reuses that schema rather than defining a new one, so this is the
// conformance coverage for a real, confirmed transport-probe outcome.
func TestOpenAPIRenderTransportProbeResponseMatchesRealResponse(t *testing.T) {
	renderCommandConfirmDeadline = 2 * time.Second
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	c := newOpenAPICompiler(t)
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	go func() {
		time.Sleep(50 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{
			surfaceTransportAvailableObs("media-01", "wall-1", false, testNow.Add(time.Second), testNow.Add(time.Second)),
		})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/transport-probe",
		`{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "RenderCommandResponse", body)
}

// TestOpenAPIRenderApplyRefusalMatchesProblemSchema proves the asset-
// unresolved refusal is a real Problem response, not just a raw string.
func TestOpenAPIRenderApplyRefusalMatchesProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	renderPutShow(t, setup.st, "halloween-2026", "Halloween 2026")
	renderPutActiveShow(t, setup.st, "halloween-2026")
	renderPutSurface(t, setup.st, "wall-1", "halloween-2026", "media-01")

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)
}
