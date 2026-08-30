// Package fallbackreconcile is Track J's J1 build item's own background
// loop: while the coordinator is healthy, it recompiles and republishes
// each participating FPP host's ADR-048 fallback program whenever
// something relevant changes, and otherwise on a fixed interval: "the
// coordinator rebuilds and distributes this program whenever an
// active-show authorization, Cue, FPP binding, target assignment, output
// action, fallback rule, or relevant catalog revision changes. While
// healthy it also reconciles the program periodically and retries an
// unacknowledged delivery. It never creates, refreshes, or relaxes a
// fallback program during an outage" (ADR-048 decision 1).
//
// This package holds no relaxed or degraded compile path: every
// reconciliation calls the identical
// [internal/coordinator/fallbackcompile.Compile] a one-off caller would,
// so there is no second, weaker set of checks a coordinator under stress
// could fall back to. A host that cannot currently compile is left with
// whatever it last held. This loop never deletes or narrows a
// previously published program on a refusal.
package fallbackreconcile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackcompile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
)

// systemPrincipalID/Name attribute this loop's own audit entries: an
// unattended background action, never behind an authenticated request,
// so there is no real credential to name, the same "no credential of
// any form" shape internal/coordinator/resolumerecoverywiring.go's own
// automatic-restore audit write documents, without that file's heavier
// reserved-principal machinery (deletion protection, a dedicated role):
// this loop's audit entries need only a stable, readable label, not a
// principal another API surface must recognize.
const (
	systemPrincipalID   = "system-fallback-reconcile"
	systemPrincipalName = "ShowMesh fallback-program reconciler"
)

const (
	auditActionPublish = "fallback.program.publish"
	auditActionRefuse  = "fallback.program.refuse"
)

// AuditWriter is the narrow slice of identity.Service this package
// depends on: identity.Service itself satisfies it directly, with no
// adapter needed.
type AuditWriter interface {
	WriteAudit(ctx context.Context, entry identity.AuditEntry) error
}

// DefaultInterval is how often [Service.Run] reconciles every
// participating host even with no [Service.Nudge]: ADR-048 decision 1's
// "reconciles the program periodically." A ShowMesh hypothesis
// (CONTRIBUTING.md's evidence ladder), not a measured value.
const DefaultInterval = 2 * time.Minute

// Signer is [fallbackcompile.Signer], re-exported so a caller wiring this
// package needs only one import for both.
type Signer = fallbackcompile.Signer

// Service is this package's own reconciliation loop: on its own tick
// interval and on every [Service.Nudge], it recompiles and republishes
// every participating FPP host's fallback program, on
// [internal/coordinator/assetsync.Service]'s identical tick-or-nudge
// shape next door: see that type's own doc comment for the pattern
// this one repeats.
type Service struct {
	st     *store.Store
	signer Signer
	audit  AuditWriter
	logger *slog.Logger
	now    func() time.Time

	interval time.Duration
	nudge    chan struct{}
}

// NewService constructs a [Service]. audit may be nil: a coordinator that
// has not wired an identity service still reconciles and publishes, it
// merely reports nothing to the audit log, matching every other
// best-effort audit write in this codebase's own posture (a missing
// dependency degrades observability, never the underlying action).
func NewService(st *store.Store, signer Signer, audit AuditWriter, logger *slog.Logger, interval time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Service{
		st: st, signer: signer, audit: audit, logger: logger, now: time.Now,
		interval: interval, nudge: make(chan struct{}, 1),
	}
}

// Nudge requests an immediate reconciliation pass, coalescing: a Nudge
// while one is already pending is a no-op, matching
// [internal/coordinator/assetsync.Service.Nudge]'s identical shape.
func (s *Service) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Run reconciles once immediately, then on every tick of interval or
// every [Service.Nudge], until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	s.reconcileOnce(ctx)
	for {
		timer := time.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.reconcileOnce(ctx)
		case <-s.nudge:
			timer.Stop()
			s.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce compiles and, on success, publishes the current program
// for every participating FPP host. A refusal is logged and audited, and
// leaves whatever was previously published untouched, TRACK-J-fpp-fallback.md
// J1: "A refusal is a visible, reported condition, never a silently
// smaller program."
func (s *Service) reconcileOnce(ctx context.Context) {
	hosts, err := fallbackcompile.ParticipatingFPPHosts(ctx, s.st)
	if err != nil {
		s.logger.Warn("fallback reconcile: list participating fpp hosts failed", "error", err)
		return
	}
	for _, instanceUUID := range hosts {
		s.reconcileHost(ctx, instanceUUID)
	}
}

func (s *Service) reconcileHost(ctx context.Context, instanceUUID string) {
	now := s.now()
	result, err := fallbackcompile.Compile(ctx, s.st, s.signer, instanceUUID, now)
	if err != nil {
		s.logger.Warn("fallback reconcile: compile failed", "fppInstanceUuid", instanceUUID, "error", err)
		return
	}
	if result.Outcome != fallbackcompile.OutcomePublished {
		s.logger.Warn("fallback reconcile: compile refused", "fppInstanceUuid", instanceUUID,
			"outcome", string(result.Outcome), "reason", result.Reason)
		s.writeAudit(ctx, identity.AuditEntry{
			Timestamp: now, PrincipalID: systemPrincipalID, PrincipalName: systemPrincipalName,
			Action: auditActionRefuse, Target: instanceUUID, Kind: identity.AuditOutcome,
			Params:        map[string]any{"outcome": string(result.Outcome)},
			OutcomeReason: result.Reason,
		})
		return
	}

	changed, err := s.publishIfChanged(ctx, result.Program)
	if err != nil {
		s.logger.Warn("fallback reconcile: publish failed", "fppInstanceUuid", instanceUUID, "error", err)
		return
	}
	if !changed {
		return
	}
	s.writeAudit(ctx, identity.AuditEntry{
		Timestamp: now, PrincipalID: systemPrincipalID, PrincipalName: systemPrincipalName,
		Action: auditActionPublish, Target: instanceUUID, Kind: identity.AuditOutcome,
		Params:        map[string]any{"packageId": result.Program.Program.PackageID, "revision": result.Program.Program.Revision},
		OutcomeReason: "published",
	})
}

// publishIfChanged stores signed as instanceUUID's current fallback
// program only when its revision differs from what is already stored (or
// nothing is stored yet), a healthy coordinator's periodic
// reconciliation against unchanged inputs must stay a no-op, never a
// republish-with-a-new-PackageID storm every [DefaultInterval].
func (s *Service) publishIfChanged(ctx context.Context, signed *fallbackprogram.SignedProgram) (bool, error) {
	instanceUUID := signed.Program.FPPInstanceUUID
	existing, err := s.st.GetFallbackProgram(ctx, instanceUUID)
	if err != nil && !errors.Is(err, store.ErrFallbackProgramNotFound) {
		return false, err
	}
	if err == nil && existing.Revision == signed.Program.Revision {
		return false, nil
	}

	raw, err := marshalSignedProgram(signed)
	if err != nil {
		return false, err
	}
	if err := s.st.PutFallbackProgram(ctx, store.FallbackProgramRecord{
		FPPInstanceUUID: instanceUUID, PackageID: signed.Program.PackageID, Revision: signed.Program.Revision,
		ShowID: signed.Program.Show, Generation: signed.Program.Generation,
		ProgramJSON: raw, SignatureB64: encodeSignature(signed.Signature),
		ExpiresAt: signed.Program.ExpiresAt, CompiledAt: signed.Program.CompiledAt,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) writeAudit(ctx context.Context, entry identity.AuditEntry) {
	if s.audit == nil {
		return
	}
	if err := s.audit.WriteAudit(ctx, entry); err != nil {
		s.logger.Warn("fallback reconcile: audit write failed", "action", entry.Action, "target", entry.Target, "error", err)
	}
}

// marshalSignedProgram and encodeSignature are this package's one
// serialization of a [fallbackprogram.SignedProgram] into
// [store.FallbackProgramRecord]'s two string columns, the same bytes a
// GET route later hands back verbatim (schemaV23's own doc comment: "the
// exact bytes a re-fetch replays, never re-serialized at read time").
func marshalSignedProgram(signed *fallbackprogram.SignedProgram) (string, error) {
	raw, err := json.Marshal(signed)
	if err != nil {
		return "", fmt.Errorf("fallbackreconcile: marshal signed program: %w", err)
	}
	return string(raw), nil
}

func encodeSignature(sig []byte) string {
	return base64.StdEncoding.EncodeToString(sig)
}
