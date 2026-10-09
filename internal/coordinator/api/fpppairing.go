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
// creates or reuses a machine principal, which is principal:write's own
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
// waiting plugin polls every three seconds, so a ceiling near one
// plugin's own rate would lock two plugins behind one NAT or proxy out of
// each other's pairings; this leaves room for several of them and still
// bounds a guessing run to a rate no attacker can exhaust an 8-character
// code space with.
const fppPairingClaimsPerMinute = 120

// fppPairingPrincipalPrefix names the machine principal one FPP plugin
// authenticates as.
const fppPairingPrincipalPrefix = "fpp-plugin-"

// fppPairingStates are this route's three operator-visible words.
const (
	fppPairingStateNone    = "none"
	fppPairingStateWaiting = "waiting"
	fppPairingStatePaired  = "paired"
)

// crockfordAlphabet is the code alphabet, upper case, with no I, L, O or
// U, so the characters an operator most often misreads cannot occur.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordBase32 is the alphabet the plugin and the coordinator both
// derive a pairing code with. Upper case, no I, L, O or U.
var crockfordBase32 = base32.NewEncoding(crockfordAlphabet).WithPadding(base32.NoPadding)

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

// normalizeFPPPairingCode turns what an operator typed into the one
// written form, or reports ok false. Letter case, a missing dash and
// stray spaces are all accepted: the code is read off a screen and typed
// by hand, and refusing it over punctuation would only teach an operator
// that pairing is flaky.
func normalizeFPPPairingCode(code string) (string, bool) {
	var body strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue
		case strings.ContainsRune(crockfordAlphabet, r):
			body.WriteRune(r)
		default:
			return "", false
		}
	}
	if body.Len() != 8 {
		return "", false
	}
	out := body.String()
	return out[:4] + "-" + out[4:], true
}

// fppPendingPairing is one open pairing, held in memory only. It holds no
// credential: the token is minted when the plugin proves it holds the secret.
type fppPendingPairing struct {
	code        string
	instanceID  string
	principalID string
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

// pendingElsewhere reports the other instance that already has this code
// waiting, so one code never matches two rows.
func (s *fppPairingStore) pendingElsewhere(code, instanceID string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingElsewhereLocked(code, instanceID, now)
}

func (s *fppPairingStore) pendingElsewhereLocked(code, instanceID string, now time.Time) (string, bool) {
	for id, p := range s.pending {
		if id != instanceID && p.code == code && now.Before(p.expiresAt) {
			return id, true
		}
	}
	return "", false
}

// open records a pending pairing, replacing any earlier one for the same
// instance. When another instance already has the same code waiting it
// records nothing and returns that instance's ID.
func (s *fppPairingStore) open(p fppPendingPairing, now time.Time) (otherInstance string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if other, ok := s.pendingElsewhereLocked(p.code, p.instanceID, now); ok {
		return other
	}
	s.pending[p.instanceID] = p
	return ""
}

// markPaired records the instance as paired once its token has been minted.
func (s *fppPairingStore) markPaired(p fppPendingPairing, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paired[p.instanceID] = fppPairedRecord{principalID: p.principalID, pairedAt: now}
}

// restore puts a consumed pairing back after a failed token mint, unless the
// operator has opened a newer one for the same instance in the meantime.
func (s *fppPairingStore) restore(p fppPendingPairing) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.pending[p.instanceID]; !taken {
		s.pending[p.instanceID] = p
	}
}

// sweepExpired drops every pending pairing that has expired. Called from
// every pairing route, so an abandoned pairing is cleaned up by the next
// one rather than waiting for its own instance to be asked about.
func (s *fppPairingStore) sweepExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pending {
		if !now.Before(p.expiresAt) {
			delete(s.pending, id)
		}
	}
}

// claim consumes the unexpired pending pairing whose code matches. A code that matches nothing, or matches
// an expired entry, reports ok false and changes nothing an attacker can
// observe.
func (s *fppPairingStore) claim(code string, now time.Time) (claimed fppPendingPairing, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for instanceID, p := range s.pending {
		if !now.Before(p.expiresAt) || subtle.ConstantTimeCompare([]byte(p.code), []byte(code)) != 1 {
			continue
		}
		delete(s.pending, instanceID)
		return p, true
	}
	return fppPendingPairing{}, false
}

// state reports one instance's pairing state, dropping an expired pending
// entry as it reads it.
func (s *fppPairingStore) state(instanceID string, now time.Time) (state string, pending fppPendingPairing, paired fppPairedRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p, ok := s.pending[instanceID]; ok && now.Before(p.expiresAt) {
		return fppPairingStateWaiting, p, fppPairedRecord{}
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

	// Every source is swept, not only this one: a map keyed by client
	// address that is only ever appended to grows without bound on a
	// route anyone on the network can reach.
	for other, times := range l.hits {
		if other == source {
			continue
		}
		if kept := keepAfter(times, cutoff); len(kept) == 0 {
			delete(l.hits, other)
		} else {
			l.hits[other] = kept
		}
	}

	kept := keepAfter(l.hits[source], cutoff)
	if len(kept) >= fppPairingClaimsPerMinute {
		l.hits[source] = kept
		return false
	}
	l.hits[source] = append(kept, now)
	return true
}

// keepAfter returns the timestamps in times that are still inside the
// window, reusing times' own backing array.
func keepAfter(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// handleStartFPPPairing serves POST /api/v1/fpp/{instanceId}/pairing.
// Creates or reuses the instance's machine principal and holds the code the
// operator supplied in memory for ten minutes. No token exists until the
// plugin claims.
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
	code, ok := normalizeFPPPairingCode(req.Code)
	if !ok {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"the pairing code is not eight characters from the plugin's own alphabet. Read the code off the plugin's page and enter it again."))
		return
	}

	h.fppPairings.sweepExpired(now)

	if other, taken := h.fppPairings.pendingElsewhere(code, instanceID, now); taken {
		writeProblem(w, h.logger, now, fppPairingCodeTakenProblem(other))
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

	principal, problem, err := h.ensureFPPPluginPrincipal(ctx, instanceID)
	if err != nil {
		h.writeInternalError(w, now, "ensure the fpp plugin principal for a pairing", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	expiresAt := now.Add(fppPairingTTL)
	pending := fppPendingPairing{code: code, instanceID: instanceID, principalID: principal.ID, expiresAt: expiresAt}
	if other := h.fppPairings.open(pending, now); other != "" {
		writeProblem(w, h.logger, now, fppPairingCodeTakenProblem(other))
		return
	}

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
func (h *handlers) ensureFPPPluginPrincipal(ctx context.Context, instanceID string) (identity.Principal, *v1.Problem, error) {
	name := fppPairingPrincipalPrefix + instanceID
	existing, err := h.deps.Identity.ListPrincipals(ctx)
	if err != nil {
		return identity.Principal{}, nil, fmt.Errorf("api: listing principals for a pairing: %w", err)
	}
	for _, p := range existing {
		if p.Name != name {
			continue
		}
		// A name match is not enough to mint a credential against. A
		// disabled account was switched off deliberately, and one whose
		// role or kind was changed is no longer the machine account this
		// route owns, so re-crediting either would quietly undo a
		// decision an administrator made.
		switch {
		case p.Disabled:
			return identity.Principal{}, problemPtr(fppPairingPrincipalUnusableProblem(
				fmt.Sprintf("the account this plugin signs in as, %q, is switched off. Turn it back on, or delete it and pair again.", name))), nil
		case p.Role != identity.RoleScheduler:
			return identity.Principal{}, problemPtr(fppPairingPrincipalUnusableProblem(
				fmt.Sprintf("the account this plugin signs in as, %q, has the %q role instead of scheduler. Set it back to scheduler, or delete it and pair again.", name, p.Role))), nil
		case p.Kind != identity.KindMachine:
			return identity.Principal{}, problemPtr(fppPairingPrincipalUnusableProblem(
				fmt.Sprintf("the account this plugin signs in as, %q, is a person's account, not a machine's. Rename or delete it and pair again.", name))), nil
		}
		return p, nil, nil
	}
	p, err := h.deps.Identity.CreatePrincipal(ctx, name, identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		return identity.Principal{}, nil, fmt.Errorf("api: creating the principal for a pairing: %w", err)
	}
	return p, nil, nil
}

// problemPtr exists because a switch arm cannot take the address of a
// function's return value.
func problemPtr(p v1.Problem) *v1.Problem { return &p }

// revokeEarlierFPPPluginTokens revokes every token the plugin's principal
// held before the one just minted, so a re-pair leaves exactly one live
// token. It returns the IDs it revoked.
func (h *handlers) revokeEarlierFPPPluginTokens(ctx context.Context, principalID, keepTokenID, instanceID string) []string {
	tokens, err := h.deps.Identity.ListTokens(ctx, principalID)
	if err != nil {
		h.logWarn("failed to list the earlier tokens of a re-paired fpp plugin", "instanceId", instanceID, "error", err)
		return nil
	}
	var revoked []string
	for _, t := range tokens {
		if t.ID == keepTokenID {
			continue
		}
		if err := h.deps.Identity.RevokeToken(ctx, t.ID); err != nil {
			h.logWarn("failed to revoke an earlier token of a re-paired fpp plugin", "instanceId", instanceID, "error", err)
			continue
		}
		revoked = append(revoked, t.ID)
	}
	return revoked
}

// handleGetFPPPairing serves GET /api/v1/fpp/{instanceId}/pairing.
func (h *handlers) handleGetFPPPairing(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	h.fppPairings.sweepExpired(now)

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
// This is one of two writes that take no principal (node enrollment redeem
// is the other): the secret in the body is the credential, and nothing else
// about the request is trusted. Every refusal is the same 404 with the same text, so a caller
// learns nothing from the difference between a wrong secret, an expired
// pairing and one that was never opened.
func (h *handlers) handleClaimFPPPairing(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	if r.ContentLength > maxFPPPairingRequestBodyBytes {
		writeProblem(w, h.logger, now, fppPairingClaimTooLargeProblem())
		return
	}
	if !h.fppPairingClaims.allow(loginSource(r), now) {
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

	// The token is minted only now, and never expires, so an unclaimed
	// pairing holds no credential and a paired plugin keeps working.
	principal, problem, err := h.ensureFPPPluginPrincipal(ctx, pending.instanceID)
	if err != nil {
		h.fppPairings.restore(pending)
		h.writeInternalError(w, now, "ensure the fpp plugin principal for a claim", err)
		return
	}
	if problem != nil {
		h.logWarn("refused to mint a token for a paired fpp plugin", "instanceId", pending.instanceID, "reason", problem.Detail)
		writeProblem(w, h.logger, now, fppPairingNotWaitingProblem())
		return
	}
	token, err := h.deps.Identity.IssueToken(ctx, principal.ID, "pairing "+formatTime(now), nil)
	if err != nil {
		h.fppPairings.restore(pending)
		h.writeInternalError(w, now, "issue the fpp plugin token for a claim", err)
		return
	}
	h.fppPairings.markPaired(pending, now)
	revoked := h.revokeEarlierFPPPluginTokens(ctx, principal.ID, token.ID, pending.instanceID)

	// Recorded after the claim has already taken: an audit store that is
	// down must not un-pair a plugin that is now holding the token.
	reason := "the plugin presented the matching secret and received its own token"
	if len(revoked) > 0 {
		reason += "; revoked earlier tokens " + strings.Join(revoked, ", ")
	}
	entry := identity.AuditEntry{
		Timestamp: now, PrincipalID: principal.ID,
		PrincipalName: fppPairingPrincipalPrefix + pending.instanceID,
		Form:          identity.FormToken, ClientAddr: h.clientAddr(r),
		Action: auditActionFPPPair, Target: pending.instanceID,
		Kind: identity.AuditOutcome, Outcome: outcomeWordConfirmed,
		OutcomeReason: reason,
	}
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.logWarn("failed to audit a completed fpp pairing", "instanceId", pending.instanceID, "error", err)
	}

	jsonWrite(w, v1.FPPPairingClaimResponse{
		Token:       token.Value,
		PrincipalID: principal.ID,
		InstanceID:  pending.instanceID,

		CoordinatorPublicKey: h.deps.CoordinatorPublicKey,
	})
}
