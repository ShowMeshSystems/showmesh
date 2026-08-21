package api

import (
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Attribution for the night controller's own autonomous dispatches.
//
// The principal who authorized the session is persisted on the session row
// as provenance and survives a restart. The controller itself dispatches as
// a constrained system actor tied to that session, never as a still-live
// user token, so no autonomous action depends on a credential outliving the
// command that started the night.

// nightControllerPrincipalPrefix names the constrained system actor the
// night controller dispatches as. It is not a principal in the identity
// store and can hold no credential.
const nightControllerPrincipalPrefix = "night-controller:"

// nightControllerIssuer is the issuer every autonomous night dispatch
// uses. It is never zero, so a cue is never silently skipped for want of
// attribution; nightAttributionMissing reports separately whether the
// authorizing principal is recorded.
func nightControllerIssuer(rec store.NightSessionRecord) FPPCommandIssuer {
	return FPPCommandIssuer{
		PrincipalID:   nightControllerPrincipalPrefix + rec.ID,
		PrincipalName: nightControllerPrincipalName(rec),
	}
}

func nightControllerPrincipalName(rec store.NightSessionRecord) string {
	if rec.Issuer.PrincipalName == "" {
		return "night controller (no authorizing principal recorded)"
	}
	return "night controller for " + rec.Issuer.PrincipalName
}

// nightAttributionMissing reports a session whose authorizing principal was
// never recorded. Its dispatches still run; the session carries
// attributionDegraded so the gap is visible rather than silent.
func nightAttributionMissing(rec store.NightSessionRecord) bool {
	return rec.Issuer.IsZero()
}

// nightIssuerFromAudit adapts an identity.AuditEntry into the persisted
// provenance shape.
func nightIssuerFromAudit(e identity.AuditEntry, command string, now time.Time) store.NightSessionIssuer {
	recordedAt := now
	return store.NightSessionIssuer{
		PrincipalID:   e.PrincipalID,
		PrincipalName: e.PrincipalName,
		Form:          string(e.Form),
		CredentialID:  e.CredentialID,
		Command:       command,
		RecordedAt:    &recordedAt,
	}
}
