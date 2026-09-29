package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/enrollment"
)

func TestOpenAPINodeEnrollmentDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"CreateNodeEnrollmentRequest", "CreateNodeEnrollmentResponse", "NodeEnrollment",
		"NodeEnrollmentsResponse", "NodeEnrollmentResponse",
		"RedeemNodeEnrollmentRequest", "RedeemNodeEnrollmentResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPINodeEnrollmentResponsesMatchRealResponses validates every
// success body and every problem class these routes produce against the
// document.
func TestOpenAPINodeEnrollmentResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	h := newEnrollHarness(t, enrollment.Config{}, newBrokerConfigDir(t, ""))

	minted, raw := h.mint(t, `{"nodeId":"render-01","expiresInSeconds":120}`, http.StatusCreated)
	assertMatchesSchema(t, c, "CreateNodeEnrollmentResponse", raw)

	_, raw = h.do(t, http.MethodGet, "/api/v1/node-enrollments", "", h.admin, "")
	assertMatchesSchema(t, c, "NodeEnrollmentsResponse", raw)

	resp, raw := h.redeem(t, minted.Code)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("redeem: %d %s", resp.StatusCode, raw)
	}
	assertMatchesSchema(t, c, "RedeemNodeEnrollmentResponse", raw)

	_, raw = h.do(t, http.MethodGet, "/api/v1/node-enrollments", "", h.admin, "")
	assertMatchesSchema(t, c, "NodeEnrollmentsResponse", raw)

	problems := map[string][]byte{}
	_, problems["409 already enrolled"] = h.mint(t, `{"nodeId":"render-01"}`, http.StatusConflict)
	_, problems["410 used"] = h.redeem(t, minted.Code)
	_, problems["404 unknown"] = h.redeem(t, "ZZZZ-ZZZZ")
	_, problems["400 malformed"] = h.redeem(t, "nope")

	pending, _ := h.mint(t, `{"nodeId":"render-02"}`, http.StatusCreated)
	resp, raw = h.do(t, http.MethodDelete, "/api/v1/node-enrollments/"+pending.ID, "", h.admin, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d %s", resp.StatusCode, raw)
	}
	assertMatchesSchema(t, c, "NodeEnrollmentResponse", raw)
	_, problems["409 not pending"] = h.do(t, http.MethodDelete, "/api/v1/node-enrollments/"+pending.ID, "", h.admin, "")

	expiring, _ := h.mint(t, `{"nodeId":"render-03","expiresInSeconds":60}`, http.StatusCreated)
	h.clock.advance(2 * time.Minute)
	_, problems["410 expired"] = h.redeem(t, expiring.Code)
	for i := 0; i < 5; i++ {
		h.redeem(t, "ZZZZ-ZZZZ")
	}
	_, problems["429 limited"] = h.redeem(t, "ZZZZ-ZZZZ")

	unavailable := newEnrollHarness(t, enrollment.Config{}, "")
	_, problems["503 no broker dir"] = unavailable.mint(t, `{"nodeId":"render-01"}`, http.StatusServiceUnavailable)

	for name, body := range problems {
		t.Run(name, func(t *testing.T) { assertMatchesSchema(t, c, "Problem", body) })
	}
}
