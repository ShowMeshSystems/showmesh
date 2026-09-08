package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track J's J1 own conformance coverage, following
// openapi_cuecatalog_test.go's exact pattern next door: every schema
// this build item added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

// TestOpenAPIFallbackProgramsDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this build item added.
func TestOpenAPIFallbackProgramsDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"FallbackProgramRenderActivation", "FallbackProgramAudioActivation", "FallbackProgramTarget",
		"FallbackProgramEntry", "FallbackProgramRules", "FallbackProgramBody", "FallbackProgramResponse",
		"FallbackProgramListEntry", "FallbackProgramListResponse",
		"FallbackProgramAcknowledgeRequest", "FallbackProgramAcknowledgeResponse",
	} {
		compileSchema(t, c, name)
	}
}

// fallbackProgramAdminAPI mirrors assetManifestAdminAPI, additionally
// wiring Dependencies.FallbackPrograms against the same store, the one
// field this build item adds.
func fallbackProgramAdminAPI(t *testing.T) (*API, *store.Store, map[string]string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.FallbackPrograms = st
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	return api, st, map[string]string{"Authorization": "Bearer " + token}
}

type fallbackProgramListEntryForTest struct {
	FPPInstanceUUID string `json:"fppInstanceUuid"`
	PackageID       string `json:"packageId"`
	Revision        string `json:"revision"`
}

type fallbackProgramListResponseForTest struct {
	ServerTime string                            `json:"serverTime"`
	Programs   []fallbackProgramListEntryForTest `json:"programs"`
}

type fallbackProgramResponseForTest struct {
	ServerTime          string          `json:"serverTime"`
	FPPInstanceUUID     string          `json:"fppInstanceUuid"`
	Published           bool            `json:"published"`
	Program             json.RawMessage `json:"program"`
	SignatureBase64     string          `json:"signatureBase64"`
	AcknowledgedStatus  string          `json:"acknowledgedStatus"`
	AcknowledgedPackage *string         `json:"acknowledgedPackageId"`
	AcknowledgedAt      *string         `json:"acknowledgedAt"`
}

type fallbackProgramAcknowledgeResponseForTest struct {
	ServerTime      string `json:"serverTime"`
	FPPInstanceUUID string `json:"fppInstanceUuid"`
	AcknowledgedAt  string `json:"acknowledgedAt"`
}

// TestOpenAPIFallbackProgramsResponsesMatchRealResponses walks every
// response shape this route family can produce against a real
// coordinator wiring: never-published, published (inserted directly into
// the store, standing in for internal/coordinator/fallbackreconcile's own
// background write, exactly as this build item's own compiler tests build a
// [store.FallbackProgramRecord]), and the three-way acknowledgement
// verdict.
func TestOpenAPIFallbackProgramsResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, st, auth := fallbackProgramAdminAPI(t)
	const instanceUUID = "M4-7840e12f81da4191c0d00fbb6a889314"

	// Before anything is published: an empty listing and an honest
	// Published=false, never a fabricated program.
	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs", auth)
	assertMatchesSchema(t, c, "FallbackProgramListResponse", listBody)
	var emptyList fallbackProgramListResponseForTest
	if err := json.Unmarshal(listBody, &emptyList); err != nil {
		t.Fatalf("decode empty fallback-programs list: %v", err)
	}
	if len(emptyList.Programs) != 0 {
		t.Fatalf("GET fallback-programs before any publish: Programs = %+v, want empty", emptyList.Programs)
	}

	_, unpublishedBody := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/"+instanceUUID, auth)
	assertMatchesSchema(t, c, "FallbackProgramResponse", unpublishedBody)
	var unpublished fallbackProgramResponseForTest
	if err := json.Unmarshal(unpublishedBody, &unpublished); err != nil {
		t.Fatalf("decode unpublished fallback program response: %v", err)
	}
	if unpublished.Published {
		t.Fatalf("GET fallback-programs/%s before any publish: Published = true, want false", instanceUUID)
	}
	if unpublished.AcknowledgedStatus != "fallback-program-unacknowledged" {
		t.Fatalf("GET fallback-programs/%s before any publish: AcknowledgedStatus = %q, want fallback-program-unacknowledged", instanceUUID, unpublished.AcknowledgedStatus)
	}

	// Insert a published program directly, standing in for the background
	// reconciler's own write. The "program" sub-object is deliberately
	// written with unusual whitespace and a REORDERED key
	// (fppInstanceUuid before schemaVersion) that plain re-marshaling
	// through a Go struct would normalize away: this is what makes the
	// byte-for-byte assertion below prove verbatim serving rather than
	// merely "the response happens to look right."
	const expectedProgramBytes = `{  "fppInstanceUuid":"` + instanceUUID + `",  "schemaVersion":1,"packageId":"pkg-1","revision":"rev-1",` +
		`"expiresAt":"2026-08-30T12:15:00Z","compiledAt":"2026-08-30T12:00:00Z",` +
		`"show":"halloween-2026","generation":1,` +
		`"playlistRevisions":{"main":1},"catalogRevisions":{"render-01":"catalog-rev-1"},` +
		`"entries":[{"entryKey":"entry-key-1","cueId":"thriller","cueRevision":1,` +
		`"targets":[{"nodeId":"render-01","render":{"sequence":"thriller","filename":"thriller.fseq","assetHashes":["aaaa"]}}]}],` +
		`"rules":{"fallbackBoundary":"safe-playback-boundary","restHold":"hold","localShutdown":"local-shutdown","recoveryBoundary":"next-scheduled-show-boundary"}}`
	programJSON := `{"program":` + expectedProgramBytes + `,"signature":"dGVzdC1zaWduYXR1cmU="}`
	expiresAt, err := time.Parse(time.RFC3339, "2026-08-30T12:15:00Z")
	if err != nil {
		t.Fatalf("parse expiresAt: %v", err)
	}
	compiledAt, err := time.Parse(time.RFC3339, "2026-08-30T12:00:00Z")
	if err != nil {
		t.Fatalf("parse compiledAt: %v", err)
	}
	if err := st.PutFallbackProgram(context.Background(), store.FallbackProgramRecord{
		FPPInstanceUUID: instanceUUID, PackageID: "pkg-1", Revision: "rev-1",
		ShowID: "halloween-2026", Generation: 1, ProgramJSON: programJSON, SignatureB64: "dGVzdC1zaWduYXR1cmU=",
		ExpiresAt: expiresAt, CompiledAt: compiledAt,
	}); err != nil {
		t.Fatalf("PutFallbackProgram: %v", err)
	}

	_, listAfterBody := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs", auth)
	assertMatchesSchema(t, c, "FallbackProgramListResponse", listAfterBody)
	var listAfter fallbackProgramListResponseForTest
	if err := json.Unmarshal(listAfterBody, &listAfter); err != nil {
		t.Fatalf("decode fallback-programs list after publish: %v", err)
	}
	if len(listAfter.Programs) != 1 || listAfter.Programs[0].FPPInstanceUUID != instanceUUID {
		t.Fatalf("GET fallback-programs after publish: Programs = %+v, want exactly one row for %s", listAfter.Programs, instanceUUID)
	}

	_, publishedBody := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/"+instanceUUID, auth)
	assertMatchesSchema(t, c, "FallbackProgramResponse", publishedBody)
	var published fallbackProgramResponseForTest
	if err := json.Unmarshal(publishedBody, &published); err != nil {
		t.Fatalf("decode published fallback program response: %v", err)
	}
	if !published.Published || published.Program == nil {
		t.Fatalf("GET fallback-programs/%s after publish: Published = %v, Program = %s, want true and non-nil", instanceUUID, published.Published, published.Program)
	}
	// The whole point of extractStoredProgramBytes: the served "program"
	// value must be identical to what was stored, VALUE for value and key
	// order included (compacted here only because jsonWrite's own
	// enclosing json.Marshal call compacts insignificant whitespace out of
	// every json.RawMessage it embeds, matching encoding/json's documented
	// behavior for that type; insignificant whitespace carries no meaning
	// for RFC 8785 canonicalization either, so that part is expected).
	// What must NOT happen, and is what this assertion actually guards
	// against, is any VALUE-level change: a re-marshal through a
	// different Go struct, the kind extractStoredProgramBytes exists to
	// avoid, would also have silently reordered these keys back to
	// struct-declaration order and could have turned a nil slice into an
	// empty one, both of which this exact-match check would catch.
	var gotCompact, wantCompact bytes.Buffer
	if err := json.Compact(&gotCompact, published.Program); err != nil {
		t.Fatalf("compact served program bytes: %v", err)
	}
	if err := json.Compact(&wantCompact, []byte(expectedProgramBytes)); err != nil {
		t.Fatalf("compact expected program bytes: %v", err)
	}
	if gotCompact.String() != wantCompact.String() {
		t.Fatalf("GET fallback-programs/%s: served program bytes are not identical to what was stored:\ngot:  %s\nwant: %s",
			instanceUUID, gotCompact.String(), wantCompact.String())
	}
	if published.SignatureBase64 != "dGVzdC1zaWduYXR1cmU=" {
		t.Fatalf("GET fallback-programs/%s: SignatureBase64 = %q, want dGVzdC1zaWduYXR1cmU=", instanceUUID, published.SignatureBase64)
	}
	if published.AcknowledgedStatus != "fallback-program-unacknowledged" {
		t.Fatalf("GET fallback-programs/%s after publish, before any ack: AcknowledgedStatus = %q, want fallback-program-unacknowledged", instanceUUID, published.AcknowledgedStatus)
	}

	// Acknowledge with the published package: fallback-program-current.
	ackReq := newJSONRequest(t, http.MethodPost, "/api/v1/fallback-programs/"+instanceUUID+"/acknowledge",
		`{"packageId":"pkg-1","revision":"rev-1","verificationResult":"verified","installedAt":"2026-08-30T12:01:00Z"}`, auth)
	ackResp, ackBody := doRawRequest(t, api.Handler, ackReq)
	if ackResp.StatusCode != http.StatusOK {
		t.Fatalf("POST fallback-programs acknowledge: status = %d, want 200; body: %s", ackResp.StatusCode, ackBody)
	}
	assertMatchesSchema(t, c, "FallbackProgramAcknowledgeResponse", ackBody)
	var ack fallbackProgramAcknowledgeResponseForTest
	if err := json.Unmarshal(ackBody, &ack); err != nil {
		t.Fatalf("decode acknowledge response: %v", err)
	}

	_, afterAckBody := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/"+instanceUUID, auth)
	assertMatchesSchema(t, c, "FallbackProgramResponse", afterAckBody)
	var afterAck fallbackProgramResponseForTest
	if err := json.Unmarshal(afterAckBody, &afterAck); err != nil {
		t.Fatalf("decode fallback program response after ack: %v", err)
	}
	if afterAck.AcknowledgedStatus != "fallback-program-current" {
		t.Fatalf("GET fallback-programs/%s after acknowledging the current package: AcknowledgedStatus = %q, want fallback-program-current", instanceUUID, afterAck.AcknowledgedStatus)
	}
	if afterAck.AcknowledgedPackage == nil || *afterAck.AcknowledgedPackage != "pkg-1" {
		t.Fatalf("GET fallback-programs/%s after ack: AcknowledgedPackage = %v, want pkg-1", instanceUUID, afterAck.AcknowledgedPackage)
	}

	// A host reporting signature-invalid for the SAME packageId/revision
	// must read back as rejected, never as current: an acknowledged-but-
	// rejected package is dishonest evidence of readiness if it is
	// reported as current merely because the identifiers happen to match.
	rejectReq := newJSONRequest(t, http.MethodPost, "/api/v1/fallback-programs/"+instanceUUID+"/acknowledge",
		`{"packageId":"pkg-1","revision":"rev-1","verificationResult":"signature-invalid","installedAt":"2026-08-30T12:02:00Z"}`, auth)
	rejectResp, rejectBody := doRawRequest(t, api.Handler, rejectReq)
	if rejectResp.StatusCode != http.StatusOK {
		t.Fatalf("POST fallback-programs acknowledge (signature-invalid): status = %d, want 200; body: %s", rejectResp.StatusCode, rejectBody)
	}
	assertMatchesSchema(t, c, "FallbackProgramAcknowledgeResponse", rejectBody)

	_, afterRejectBody := doRequest(t, api.Handler, "GET", "/api/v1/fallback-programs/"+instanceUUID, auth)
	assertMatchesSchema(t, c, "FallbackProgramResponse", afterRejectBody)
	var afterReject fallbackProgramResponseForTest
	if err := json.Unmarshal(afterRejectBody, &afterReject); err != nil {
		t.Fatalf("decode fallback program response after a rejected ack: %v", err)
	}
	if afterReject.AcknowledgedStatus != "fallback-program-rejected" {
		t.Fatalf("GET fallback-programs/%s after a signature-invalid ack with a MATCHING packageId/revision: AcknowledgedStatus = %q, want fallback-program-rejected (never current)",
			instanceUUID, afterReject.AcknowledgedStatus)
	}

	// A validation-error sample, to prove the Problem shape on this
	// route's own refusal path.
	badReq := newJSONRequest(t, http.MethodPost, "/api/v1/fallback-programs/"+instanceUUID+"/acknowledge",
		`{"packageId":"","revision":"rev-1","verificationResult":"verified","installedAt":"2026-08-30T12:01:00Z"}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST fallback-programs acknowledge with empty packageId: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}

// TestOpenAPIFallbackProgramAcknowledgeRequestBodyReferencesDocumentedSchema
// resolves the document pointer assertMatchesSchema never reads.
func TestOpenAPIFallbackProgramAcknowledgeRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/fallback-programs/{fppInstanceId}/acknowledge"); got != "FallbackProgramAcknowledgeRequest" {
		t.Errorf("POST /fallback-programs/{fppInstanceId}/acknowledge requestBody schema = %q, want FallbackProgramAcknowledgeRequest", got)
	}
}
