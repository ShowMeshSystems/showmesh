package api

import (
	"sync"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// nightIssuerRegistry attributes the night loop's own autonomous FPP
// dispatches to the principal who most recently authorized this session
// through an authenticated lifecycle command. In-memory only, never a
// stored column: nightEnsureAnchor (nightloop.go) refuses to dispatch
// when no entry exists (a zero issuer must never reach a command
// envelope or an audit entry), so losing this map on restart degrades to
// a refusal, never a false attribution.
var nightIssuerRegistry sync.Map // sessionID string -> FPPCommandIssuer

// recordNightIssuer stores issuer as the current attribution for
// sessionID. Called once per successful gated lifecycle command.
func recordNightIssuer(sessionID string, issuer FPPCommandIssuer) {
	if sessionID == "" {
		return
	}
	nightIssuerRegistry.Store(sessionID, issuer)
}

// forgetNightIssuer removes sessionID's attribution. Called on
// end-session: an ended session must not leave a stale principal
// attributed to whatever session id a later prepare-site reuses.
func forgetNightIssuer(sessionID string) {
	nightIssuerRegistry.Delete(sessionID)
}

// nightIssuerFromAudit adapts an identity.AuditEntry into the
// FPPCommandIssuer shape dispatchFPPCommand expects — a pure relabeling,
// the two types mirror each other field-for-field.
func nightIssuerFromAudit(e identity.AuditEntry) FPPCommandIssuer {
	return FPPCommandIssuer{
		PrincipalID:   e.PrincipalID,
		PrincipalName: e.PrincipalName,
		Form:          e.Form,
		CredentialID:  e.CredentialID,
		ClientAddr:    e.ClientAddr,
	}
}

// nightIssuerFor returns the last-recorded issuer for sessionID, or the
// zero FPPCommandIssuer if none is recorded — the caller
// (nightEnsureAnchor) is responsible for refusing to dispatch on a zero
// issuer rather than treating it as usable.
func nightIssuerFor(sessionID string) FPPCommandIssuer {
	v, ok := nightIssuerRegistry.Load(sessionID)
	if !ok {
		return FPPCommandIssuer{}
	}
	issuer, _ := v.(FPPCommandIssuer)
	return issuer
}
