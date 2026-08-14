package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file closes a gap this task's own brief asked to be named plainly
// rather than papered over. Before this file existed, exactly ONE
// conformance test touched this endpoint's schemas at all:
// fppcommand_handler_test.go's TestOpenAPIFPPCommandResponseMatchesRealResponse
// (singular), which validates a real stopPlaylist response — Step 7's own
// primitive, the one zero-parameter case — plus its replay, against
// FPPCommandResponse. That test predates Step 8: the other seven
// primitives this registry added, and the request side of this contract
// in either shape, had NO conformance coverage anywhere. That gap
// predates this file too — it is not something Step 8's own review
// introduced — but leaving it open after restructuring FPPCommandRequest
// into a discriminated `oneOf` (this task) would mean the schema this
// task rewrote is the one this suite still barely looks at.
//
// Stated plainly, since this task's own brief asks for it: what this file
// adds is NOT symmetric.
//
//   - TestOpenAPIFPPCommandRequestAndResponseVariantsMatchSchemas extends
//     response coverage to the three oneOf variants
//     TestOpenAPIFPPCommandResponseMatchesRealResponse's stopPlaylist case
//     never exercised (startPlaylist, stopPlaylistGracefully, setVolume),
//     each a REAL response from a REAL [API] (a real store.Store, a real
//     identity.Service, an httptest.Server standing in for FPP — exactly
//     fppcommand_handler_test.go's own scaffolding), validated against
//     FPPCommandResponse. This is "conformance-tested against real
//     handler responses" in the same sense every other entry in
//     TestOpenAPISchemasMatchRealResponses is.
//   - The REQUEST half is schema-only, and is added here for the FIRST
//     time for this endpoint: the exact wire bytes each subtest is about
//     to POST are validated against FPPCommandRequest's own oneOf schema
//     BEFORE being sent, proving the schema accepts the shape this file's
//     own dispatch helpers (fppCommandBody, newFPPCommandRequest —
//     fppcommand_handler_test.go) actually send. That is real coverage of
//     the SCHEMA — it is what proves the misspelled-param rejection this
//     task's UI-side type-level check proves at compile time is also true
//     at the JSON Schema level the Go side publishes — but it is NOT
//     "conformance-tested against a real handler response," because a
//     REQUEST is not a response any handler produced; there is nothing
//     for a "real" request to be captured FROM the way a response is
//     captured from an actual [http.Handler] round trip. Do not read this
//     file as adding request coverage of the same KIND the response half
//     has.
func TestOpenAPIFPPCommandRequestAndResponseVariantsMatchSchemas(t *testing.T) {
	c := newOpenAPICompiler(t)

	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "OK")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// idle, and nothing else: every subtest below dispatches against this
	// SAME fixed evidence, so some outcomes resolve "confirmed" (stopPlaylist,
	// whose desired idle state already matches) and some resolve
	// "unconfirmed" after their own short deadline elapses (startPlaylist,
	// stopPlaylistGracefully, setVolume, none of whose desired state this
	// evidence satisfies) — both are valid values of FPPCommandResult.outcome,
	// and this test asserts the response is well-formed either way, not
	// that every dispatch confirms.
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 60 * time.Millisecond, FPPCommandPollInterval: 5 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// One representative per oneOf variant this task's restructuring
	// added (StartPlaylistCommandRequest, StopPlaylistGracefullyCommandRequest,
	// SetVolumeCommandRequest, NoParamsFPPCommandRequest — stopPlaylist
	// stands in for that whole zero-param family, which fppCommandBody's
	// own "" case already exercises for every one of Step 7's own tests).
	tests := []struct {
		name string
		body string
	}{
		{"startPlaylist", fppCommandBody("startPlaylist", "conf-key-startPlaylist",
			`{"playlist":"showmesh-test","repeat":true,"ifBusy":"replace"}`)},
		{"stopPlaylist", fppCommandBody("stopPlaylist", "conf-key-stopPlaylist", "")},
		{"stopPlaylistGracefully", fppCommandBody("stopPlaylistGracefully", "conf-key-stopPlaylistGracefully",
			`{"afterLoop":true}`)},
		{"setVolume", fppCommandBody("setVolume", "conf-key-setVolume", `{"volume":42}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Request-side (schema-only — see this file's own top
			// comment): the exact bytes about to be sent must themselves
			// match exactly one FPPCommandRequest oneOf branch.
			assertMatchesSchema(t, c, "FPPCommandRequest", []byte(tt.body))

			req := newFPPCommandRequest(t, "bench-fpp", tt.body, token)
			resp, respBody := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
			}
			// Response-side: a REAL response from a real [API], validated
			// against FPPCommandResponse — the same guarantee every other
			// entry in TestOpenAPISchemasMatchRealResponses carries.
			assertMatchesSchema(t, c, "FPPCommandResponse", respBody)
		})
	}
}

// TestOpenAPIFPPCommandAuditUnavailableResponseMatchesRealResponse is
// ProblemTypeFPPCommandRefusedAuditUnavailable's own conformance check —
// added to api/openapi.yaml's Problem.type enum by this task (Task 2a);
// this proves a REAL 503 this endpoint actually produces
// (fppcommand_handler_test.go's own
// TestFPPCommandNonSafetyClassPrimitiveFailsClosedWithAuditFailing already
// proves the STATUS CODE and the refusal's own effects — no commands row,
// no dispatch — this test's own addition is the schema check, not a
// duplicate of that behavioral proof) validates against the shared
// Problem schema.
func TestOpenAPIFPPCommandAuditUnavailableResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)

	fppSrv := newFailIfHitFPPCommandServer(t)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	installFailAuditTrigger(t, setup.storeDir)

	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 60 * time.Millisecond, FPPCommandPollInterval: 5 * time.Millisecond,
	})

	body := fppCommandBody("startPlaylist", "conf-key-audit-unavailable", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "Problem", respBody)

	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeFPPCommandRefusedAuditUnavailable {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeFPPCommandRefusedAuditUnavailable)
	}
}

// TestOpenAPIFPPStartPlaylistEvidenceNotCurrentResponseMatchesRealResponse
// is ProblemTypeFPPStartPlaylistEvidenceNotCurrent's own conformance check
// (Task 2b), extended by Step 8 review finding 8 to also cover
// ProblemTypeFPPStartPlaylistBusy: originally both of startPlaylist's own
// 409s shared ProblemTypeConflict, distinguishable only by matching a
// substring of the server's own English `detail` text
// (FPPStartPlaylistControl.tsx's former defect); task 2b split
// evidence-not-current out but left fppStartPlaylistBusyProblem sharing
// ProblemTypeConflict with the idempotency-key-conflict constructors —
// finding 8's own "half-applied" catch. This test now proves all THREE are
// distinguishable on the wire: ProblemTypeFPPStartPlaylistEvidenceNotCurrent,
// ProblemTypeFPPStartPlaylistBusy, and ProblemTypeConflict (the idempotency
// case, exercised elsewhere in this package) are three DIFFERENT `type`
// values, all matching the shared Problem schema, from two REAL responses of
// a real [API] — not from hand-built JSON.
func TestOpenAPIFPPStartPlaylistEvidenceNotCurrentResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)

	fppSrv := newFailIfHitFPPCommandServer(t)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// No observations at all: PreDispatchCheck's own "could not tell" path.
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	notCurrentBody := fppCommandBody("startPlaylist", "conf-key-evidence-not-current", `{"playlist":"showmesh-test"}`)
	notCurrentReq := newFPPCommandRequest(t, "bench-fpp", notCurrentBody, token)
	notCurrentResp, notCurrentBodyBytes := doRawRequest(t, api.Handler, notCurrentReq)
	if notCurrentResp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", notCurrentResp.StatusCode, notCurrentBodyBytes)
	}
	assertMatchesSchema(t, c, "Problem", notCurrentBodyBytes)
	notCurrentMap := decodeMap(t, notCurrentBodyBytes)
	if notCurrentMap["type"] != ProblemTypeFPPStartPlaylistEvidenceNotCurrent {
		t.Errorf("type = %v, want %v", notCurrentMap["type"], ProblemTypeFPPStartPlaylistEvidenceNotCurrent)
	}

	// The SIBLING 409 (a DIFFERENT playlist confirmed playing) now carries
	// its OWN type (finding 8), never the plain ProblemTypeConflict an
	// idempotency-key conflict uses — proving the split is real (two
	// DIFFERENT, non-"conflict" types on the same status), not merely that
	// the new type exists in isolation.
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "other-playlist", testNow, testNow),
	})
	busyBody := fppCommandBody("startPlaylist", "conf-key-busy", `{"playlist":"showmesh-test"}`)
	busyReq := newFPPCommandRequest(t, "bench-fpp", busyBody, token)
	busyResp, busyBodyBytes := doRawRequest(t, api.Handler, busyReq)
	if busyResp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", busyResp.StatusCode, busyBodyBytes)
	}
	assertMatchesSchema(t, c, "Problem", busyBodyBytes)
	busyMap := decodeMap(t, busyBodyBytes)
	if busyMap["type"] != ProblemTypeFPPStartPlaylistBusy {
		t.Errorf("type = %v, want %v (finding 8: no longer the plain, shared ProblemTypeConflict)", busyMap["type"], ProblemTypeFPPStartPlaylistBusy)
	}
	if busyMap["type"] == ProblemTypeConflict {
		t.Fatalf("busy 409 still carries the plain ProblemTypeConflict (%v) — finding 8's split is not real", busyMap["type"])
	}
	if busyMap["type"] == notCurrentMap["type"] {
		t.Fatalf("both 409s carry the SAME type (%v) — the earlier split this task also covers is not real", busyMap["type"])
	}
}

// TestOpenAPIStartPlaylistSchemaRejectsWhatTheServerAlwaysRejected is Step
// 8 review finding 12's own regression proof: before this task's fix,
// StartPlaylistCommandRequest.params.playlist declared only
// `type: string, minLength: 1` — every one of the three bodies below
// validated against the PUBLISHED schema and then got a 400 from the real
// server, meaning a generated client could construct a schema-valid
// request that always fails. The fix (`maxLength`, and the `allOf`
// pattern trio this task derived and checked against
// internal/coordinator/fppcommand.ValidatePlaylistName with a standalone
// Go program before publishing it — see this task's report) closes that
// gap without touching the server's own validation, which stays
// authoritative. This checks the SCHEMA side only (a request is not a
// response any handler produced — see this file's own top comment for why
// that is a deliberate, narrower kind of coverage than
// assertMatchesSchema's response-side checks); TestStartPlaylistRejects*
// in fppcommand_primitives_test.go already proves the server's own 400 for
// these same shapes.
func TestOpenAPIStartPlaylistSchemaRejectsWhatTheServerAlwaysRejected(t *testing.T) {
	c := newOpenAPICompiler(t)
	sch := compileSchema(t, c, "FPPCommandRequest")

	cases := []struct {
		name string
		body string
	}{
		{"leading and trailing whitespace", fppCommandBody("startPlaylist", "k", `{"playlist":" showmesh-test "}`)},
		{"exceeds 250 bytes", fppCommandBody("startPlaylist", "k", `{"playlist":"`+strings.Repeat("a", 300)+`"}`)},
		{"path traversal", fppCommandBody("startPlaylist", "k", `{"playlist":"../../etc/passwd"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := jsonschema.UnmarshalJSON(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("decoding request body for validation: %v", err)
			}
			if err := sch.Validate(instance); err == nil {
				t.Errorf("FPPCommandRequest schema accepted %s (%s) — the server always rejects this with a 400, so a "+
					"generated client could build a schema-valid request that never succeeds", tc.name, tc.body)
			}
		})
	}
}
