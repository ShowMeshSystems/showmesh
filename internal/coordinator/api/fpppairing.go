package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Pairing an FPP plugin with this coordinator. An operator opens a
// pairing with the code the plugin displays; the plugin then presents the
// secret that code was derived from and receives its own token. The
// operator never sees the token and the coordinator never sees the secret
// until the plugin itself sends it.

// scopeFPPPairing guards the operator half of pairing: opening one
// creates a principal and mints a token, which is principal:write's own
// blast radius and admin-only.
var scopeFPPPairing = identity.ScopePrincipalWrite

// auditActionFPPPair is the audit action both halves of a pairing write.
const auditActionFPPPair = "fpp.pair"

// fppPairingTTL is how long a pairing stays open after an operator starts
// it, matching the plugin worker's own expiry.
const fppPairingTTL = 10 * time.Minute

// maxFPPPairingRequestBodyBytes bounds both pairing bodies: a code and a
// hex secret have no legitimate reason to be large.
const maxFPPPairingRequestBodyBytes = 4 << 10 // 4 KiB

// fppPairingClaimsPerMinute is the per-client-address claim ceiling. A
// waiting plugin polls every three seconds, so this leaves an order of
// magnitude of headroom while still bounding a guessing run.
const fppPairingClaimsPerMinute = 30

// fppPairingPrincipalPrefix names the machine principal one FPP plugin
// authenticates as.
const fppPairingPrincipalPrefix = "fpp-plugin-"

// fppPairingStates are this route's three operator-visible words.
const (
	fppPairingStateNone    = "none"
	fppPairingStateWaiting = "waiting"
	fppPairingStatePaired  = "paired"
)

// crockfordBase32 is the alphabet the plugin and the coordinator both
// derive a pairing code with. Upper case, no I, L, O or U.
var crockfordBase32 = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// fppPairingCodeFromSecret derives the displayed code from the plugin's
// own secret: the first eight Crockford base32 characters of the
// SHA-256 of the secret's 32 bytes, written XXXX-XXXX.
func fppPairingCodeFromSecret(secretHex string) (string, bool) {
	raw, err := hex.DecodeString(secretHex)
	if err != nil || len(raw) != 32 || strings.ToLower(secretHex) != secretHex {
		return "", false
	}
	sum := sha256.Sum256(raw)
	encoded := crockfordBase32.EncodeToString(sum[:5])
	return encoded[:4] + "-" + encoded[4:8], true
}

// validFPPPairingCode reports whether code is syntactically a pairing
// code. It says nothing about whether a pairing is open for it.
func validFPPPairingCode(code string) bool {
	if len(code) != 9 || code[4] != '-' {
		return false
	}
	for i, r := range code {
		if i == 4 {
			continue
		}
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
			return false
		}
	}
	return true
}

// fppPendingPairing is one open pairing, held in memory only. Token is
// the minted secret and never leaves this process except in the claim
// response to the plugin that proved it holds the matching secret.
type fppPendingPairing struct {
	code        string
	instanceID  string
	principalID string
	token       string
	expiresAt   time.Time
}

// fppPairedRecord is what this process remembers about a completed
// pairing. It holds no credential.
type fppPairedRecord struct {
	principalID string
	pairedAt    time.Time
}

// fppPairingStore holds open and completed pairings for this process's
// lifetime. Nothing here is persisted: a coordinator restart loses the
// open pairing (which expires in ten minutes anyway) and the remembered
// paired state, while the token the plugin already holds keeps working.
type fppPairingStore struct {
	mu      sync.Mutex
	pending map[string]fppPendingPairing
	paired  map[string]fppPairedRecord
}

func newFPPPairingStore() *fppPairingStore {
	return &fppPairingStore{
		pending: make(map[string]fppPendingPairing),
		paired:  make(map[string]fppPairedRecord),
	}
}

// open records a pending pairing, replacing any earlier one for the same
// instance.
func (s *fppPairingStore) open(p fppPendingPairing) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[p.instanceID] = p
}

// claim consumes the unexpired pending pairing whose code matches, and
// records the instance as paired. A code that matches nothing, or matches
// an expired entry, reports ok false and changes nothing an attacker can
// observe.
func (s *fppPairingStore) claim(code string, now time.Time) (fppPendingPairing, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for instanceID, p := range s.pending {
		if !now.Before(p.expiresAt) {
			delete(s.pending, instanceID)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(p.code), []byte(code)) != 1 {
			continue
		}
		delete(s.pending, instanceID)
		s.paired[instanceID] = fppPairedRecord{principalID: p.principalID, pairedAt: now}
		return p, true
	}
	return fppPendingPairing{}, false
}

// state reports one instance's pairing state, dropping an expired pending
// entry as it reads it.
func (s *fppPairingStore) state(instanceID string, now time.Time) (state string, pending fppPendingPairing, paired fppPairedRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p, ok := s.pending[instanceID]; ok {
		if now.Before(p.expiresAt) {
			return fppPairingStateWaiting, p, fppPairedRecord{}
		}
		delete(s.pending, instanceID)
	}
	if rec, ok := s.paired[instanceID]; ok {
		return fppPairingStatePaired, fppPendingPairing{}, rec
	}
	return fppPairingStateNone, fppPendingPairing{}, fppPairedRecord{}
}

// fppPairingClaimLimiter bounds claims per client address over a rolling
// minute. In-memory and per-*handlers, like every other single-process
// guard in this package.
type fppPairingClaimLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newFPPPairingClaimLimiter() *fppPairingClaimLimiter {
	return &fppPairingClaimLimiter{hits: make(map[string][]time.Time)}
}

// allow records one claim from source and reports whether it is within
// the ceiling.
func (l *fppPairingClaimLimiter) allow(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-time.Minute)
	kept := l.hits[source][:0]
	for _, t := range l.hits[source] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= fppPairingClaimsPerMinute {
		l.hits[source] = kept
		return false
	}
	l.hits[source] = append(kept, now)
	return true
}

// claimSource is the client address a claim is rate limited by. It is
// never the audit client address: h.clientAddr is empty unless a trusted
// proxy is configured, and a rate limit keyed on "" would pool every
// plugin on the network into one bucket.
func claimSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handleStartFPPPairing serves POST /api/v1/fpp/{instanceId}/pairing.
// Creates or reuses the instance's machine principal, mints it a fresh
// token, and holds that token in memory for ten minutes against the code
// the operator supplied. The token is never in the response.
func (h *handlers) handleStartFPPPairing(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	var req v1.FPPPairingRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPPairingRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be JSON matching {"code":"XXXX-XXXX"}`))
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if !validFPPPairingCode(code) {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"the pairing code is not in the form XXXX-XXXX. Read the code off the plugin's own page and enter it again."))
		return
	}

	ac := authFromContext(ctx)
	if !h.writeAuditOrFail(ctx, w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPPair, Target: instanceID,
		Kind: identity.AuditDispatch, CommandID: uuid.NewString(),
		Params: map[string]any{"code": code},
	}) {
		return
	}

	principal, err := h.ensureFPPPluginPrincipal(ctx, instanceID)
	if err != nil {
		h.writeInternalError(w, now, "ensure the fpp plugin principal for a pairing", err)
		return
	}
	token, err := h.deps.Identity.IssueToken(ctx, principal.ID, "pairing "+formatTime(now), nil)
	if err != nil {
		h.writeInternalError(w, now, "issue the fpp plugin token for a pairing", err)
		return
	}

	expiresAt := now.Add(fppPairingTTL)
	h.fppPairings.open(fppPendingPairing{
		code: code, instanceID: instanceID, principalID: principal.ID,
		token: token.Value, expiresAt: expiresAt,
	})

	jsonWrite(w, v1.FPPPairingResponse{
		ServerTime:  formatTime(now),
		InstanceID:  instanceID,
		Code:        code,
		PrincipalID: principal.ID,
		State:       fppPairingStateWaiting,
		ExpiresAt:   formatTime(expiresAt),
	})
}

// ensureFPPPluginPrincipal returns the machine principal this instance's
// plugin authenticates as, creating it when it does not exist yet. A
// pairing repeated after a coordinator restart reuses the same principal
// rather than accumulating one per attempt.
func (h *handlers) ensureFPPPluginPrincipal(ctx context.Context, instanceID string) (identity.Principal, error) {
	name := fppPairingPrincipalPrefix + instanceID
	existing, err := h.deps.Identity.ListPrincipals(ctx)
	if err != nil {
		return identity.Principal{}, fmt.Errorf("api: listing principals for a pairing: %w", err)
	}
	for _, p := range existing {
		if p.Name == name {
			return p, nil
		}
	}
	p, err := h.deps.Identity.CreatePrincipal(ctx, name, identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		return identity.Principal{}, fmt.Errorf("api: creating the principal for a pairing: %w", err)
	}
	return p, nil
}

// handleGetFPPPairing serves GET /api/v1/fpp/{instanceId}/pairing.
func (h *handlers) handleGetFPPPairing(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	state, pending, paired := h.fppPairings.state(instanceID, now)
	resp := v1.FPPPairingStateResponse{ServerTime: formatTime(now), State: state}
	switch state {
	case fppPairingStateWaiting:
		resp.Code = pending.code
		resp.PrincipalID = pending.principalID
		expires := formatTime(pending.expiresAt)
		resp.ExpiresAt = &expires
	case fppPairingStatePaired:
		resp.PrincipalID = paired.principalID
		pairedAt := formatTime(paired.pairedAt)
		resp.PairedAt = &pairedAt
	}
	jsonWrite(w, resp)
}

// handleClaimFPPPairing serves POST /api/v1/integrations/fpp/pairing/claim.
// This is the only unauthenticated write route in this API: the secret in
// the body is the credential, and nothing else about the request is
// trusted. Every refusal is the same 404 with the same text, so a caller
// learns nothing from the difference between a wrong secret, an expired
// pairing and one that was never opened.
func (h *handlers) handleClaimFPPPairing(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	if r.ContentLength > maxFPPPairingRequestBodyBytes {
		writeProblem(w, h.logger, now, fppPairingClaimTooLargeProblem())
		return
	}
	if !h.fppPairingClaims.allow(claimSource(r), now) {
		w.Header().Set("Retry-After", "60")
		writeProblem(w, h.logger, now, tooManyRequestsProblem(
			"this plugin has asked to finish pairing too many times in the last minute. Wait a minute and let it try again."))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxFPPPairingRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, fppPairingNotWaitingProblem())
		return
	}
	if len(body) > maxFPPPairingRequestBodyBytes {
		writeProblem(w, h.logger, now, fppPairingClaimTooLargeProblem())
		return
	}

	var req v1.FPPPairingClaimRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeProblem(w, h.logger, now, fppPairingNotWaitingProblem())
		return
	}
	code, ok := fppPairingCodeFromSecret(req.Secret)
	if !ok {
		writeProblem(w, h.logger, now, fppPairingNotWaitingProblem())
		return
	}
	pending, ok := h.fppPairings.claim(code, now)
	if !ok {
		writeProblem(w, h.logger, now, fppPairingNotWaitingProblem())
		return
	}

	// Recorded after the claim has already taken: this entry attributes
	// the pairing to the principal it just handed a credential to, and an
	// audit store that is down must not un-pair a plugin that is now
	// holding the token.
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: pending.principalID,
		PrincipalName: fppPairingPrincipalPrefix + pending.instanceID,
		Form:          identity.FormToken, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPPair, Target: pending.instanceID,
		Kind: identity.AuditOutcome, Outcome: outcomeWordConfirmed,
		OutcomeReason: "the plugin presented the matching secret and received its own token",
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("failed to audit a completed fpp pairing", "instanceId", pending.instanceID, "error", err)
	}

	jsonWrite(w, v1.FPPPairingClaimResponse{
		Token:       pending.token,
		PrincipalID: pending.principalID,
		InstanceID:  pending.instanceID,
	})
}
