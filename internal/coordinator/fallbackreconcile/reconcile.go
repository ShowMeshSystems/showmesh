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
	"sort"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackcompile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
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

// RefreshWindow is how close to a published program's ExpiresAt
// [Service.publishIfChanged] republishes it even though its Revision has
// not changed: half of [fallbackcompile.ProgramTTL], comfortably larger
// than [DefaultInterval] so a periodic tick catches the refresh well
// before the stored program actually expires. Content identity (the
// Revision equality check) and time (this window) are deliberately two
// separate conditions that never pollute each other: an unchanged show
// still gets a fresh ExpiresAt on the SAME Revision, and a changed show
// still republishes immediately regardless of how much of its old
// window remains.
const RefreshWindow = fallbackcompile.ProgramTTL / 2

// Signer is [fallbackcompile.Signer], re-exported so a caller wiring this
// package needs only one import for both.
type Signer = fallbackcompile.Signer

// CatalogDeployer attempts to deploy nodeID's currently-required cue
// catalog when doing so is safe, called by [Service.autoDeployStaleCatalogs]
// once per candidate node after a reconcile pass observes
// fallbackcompile.OutcomeMissingCatalogAcknowledgement (Part 2's own
// trigger, see reconcileOnce/reconcileHost below). Best-effort and
// synchronous from this package's point of view: it returns nothing, and
// a slow or failing attempt for one node must never hold up the rest of
// this reconcile pass or any other host's own compile.
//
// Implemented by the API layer (internal/coordinator/api), which alone
// holds the dispatch path (cuecatalogdeploy.go) and the node-activity
// evidence a hold decision needs (cuecatalogautodeploy.go). This package
// is deliberately given no HTTP or MQTT dependency of its own to make that
// call directly, and never derives node activity or dispatches
// cuecatalog.deploy a second way.
type CatalogDeployer interface {
	AutoDeployCueCatalog(ctx context.Context, now time.Time, nodeID string)
}

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

	// catalogDeployer is Part 2's own auto-deploy trigger. See
	// [CatalogDeployer]'s own doc comment and [SetCatalogDeployer]. nil (the
	// zero value) disables auto-deploy entirely: reconcileOnce still
	// compiles, logs, and audits exactly as before this field existed.
	catalogDeployer CatalogDeployer
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

// SetCatalogDeployer wires d as this Service's own Part 2 auto-deploy
// trigger. Not a [NewService] parameter: coordinator.go constructs this
// Service well before the API layer that implements [CatalogDeployer]
// exists, and every other Service field is a plain constructor argument
// with no such ordering constraint (see that file's own comment on why
// this one is set later). Call it once, before [Service.Run] starts; it is
// not safe to call concurrently with a reconcile pass.
func (s *Service) SetCatalogDeployer(d CatalogDeployer) { s.catalogDeployer = d }

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
	var missingAck bool
	for _, instanceUUID := range hosts {
		if s.reconcileHost(ctx, instanceUUID) {
			missingAck = true
		}
	}
	// Part 2's own trigger: reuse the exact condition reconcileHost's own
	// Compile call just detected (fallbackcompile.OutcomeMissingCatalogAcknowledgement),
	// rather than a second detector on its own schedule. Compile's own
	// Result only ever names the first node it happened to refuse a given
	// host's program on, never the complete set, so autoDeployStaleCatalogs
	// resolves the full candidate list itself.
	if missingAck && s.catalogDeployer != nil {
		s.autoDeployStaleCatalogs(ctx, s.now())
	}
}

// reconcileHost compiles and, on success, publishes the current fallback
// program for instanceUUID. A refusal is logged and audited, and leaves
// whatever was previously published untouched, TRACK-J-fpp-fallback.md
// J1: "A refusal is a visible, reported condition, never a silently
// smaller program." Its own return reports only whether THIS host's
// compile refused specifically with
// fallbackcompile.OutcomeMissingCatalogAcknowledgement, reconcileOnce's
// own signal for whether an auto-deploy pass is worth running this cycle.
func (s *Service) reconcileHost(ctx context.Context, instanceUUID string) bool {
	now := s.now()
	result, err := fallbackcompile.Compile(ctx, s.st, s.signer, instanceUUID, now)
	if err != nil {
		s.logger.Warn("fallback reconcile: compile failed", "fppInstanceUuid", instanceUUID, "error", err)
		return false
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
		return result.Outcome == fallbackcompile.OutcomeMissingCatalogAcknowledgement
	}

	changed, err := s.publishIfChanged(ctx, result.Program, now)
	if err != nil {
		s.logger.Warn("fallback reconcile: publish failed", "fppInstanceUuid", instanceUUID, "error", err)
		return false
	}
	if !changed {
		return false
	}
	s.writeAudit(ctx, identity.AuditEntry{
		Timestamp: now, PrincipalID: systemPrincipalID, PrincipalName: systemPrincipalName,
		Action: auditActionPublish, Target: instanceUUID, Kind: identity.AuditOutcome,
		Params:        map[string]any{"packageId": result.Program.Program.PackageID, "revision": result.Program.Program.Revision},
		OutcomeReason: "published",
	})
	return false
}

// autoDeployStaleCatalogs resolves the active show's full participating-
// node set independently (fallbackcompile.Compile itself stops at the
// first node it finds missing an acknowledgement, so its own Result never
// names the complete candidate list), and asks s.catalogDeployer to
// attempt every node whose acknowledgement is not current, reusing
// [fppreconcile.NodeCatalogAckStatus], the one place that resolution is
// made, exactly as [fallbackcompile.Compile] itself does. The deployer
// alone decides whether a given attempt is safe (a running activation
// holds it) and performs the actual dispatch; this loop only identifies
// candidates and never touches cuecatalog.deploy itself.
func (s *Service) autoDeployStaleCatalogs(ctx context.Context, now time.Time) {
	active, err := assetsync.ResolveActiveShow(ctx, s.st)
	if err != nil {
		s.logger.Warn("fallback reconcile: resolve active show for auto-deploy failed", "error", err)
		return
	}
	if !active.Configured {
		return
	}
	nodes, err := s.st.ListNodeDeclarations(ctx)
	if err != nil {
		s.logger.Warn("fallback reconcile: list node declarations for auto-deploy failed", "error", err)
		return
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	for _, n := range nodes {
		catalog, err := assetsync.ResolveCueCatalog(ctx, s.st, active, n.NodeID)
		if err != nil {
			s.logger.Warn("fallback reconcile: resolve cue catalog for auto-deploy failed", "node", n.NodeID, "error", err)
			continue
		}
		if !catalog.HasAnyOutput() {
			continue
		}
		status, _, _, err := fppreconcile.NodeCatalogAckStatus(ctx, s.st, n.NodeID, catalog.Revision)
		if err != nil {
			s.logger.Warn("fallback reconcile: resolve node catalog ack status for auto-deploy failed", "node", n.NodeID, "error", err)
			continue
		}
		if status == v1.CueCatalogStatusCurrent {
			continue
		}
		s.catalogDeployer.AutoDeployCueCatalog(ctx, now, n.NodeID)
	}
}

// publishIfChanged stores signed as instanceUUID's current fallback
// program when EITHER of two independent conditions holds: its revision
// differs from what is already stored (or nothing is stored yet), or the
// stored program's own ExpiresAt is inside [RefreshWindow]. Content
// identity drives the first arm and time drives the second; a healthy
// coordinator's periodic reconciliation against unchanged content stays
// a no-op only while BOTH conditions say so, which is what keeps an
// unchanging show's published program from silently expiring: without
// the second arm, a revision-equality check alone freezes ExpiresAt at
// whatever it was on the last real content change, forever, the moment
// content stops changing.
func (s *Service) publishIfChanged(ctx context.Context, signed *fallbackprogram.SignedProgram, now time.Time) (bool, error) {
	instanceUUID := signed.Program.FPPInstanceUUID
	existing, err := s.st.GetFallbackProgram(ctx, instanceUUID)
	if err != nil && !errors.Is(err, store.ErrFallbackProgramNotFound) {
		return false, err
	}
	if err == nil && existing.Revision == signed.Program.Revision && now.Add(RefreshWindow).Before(existing.ExpiresAt) {
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
// GET route later hands back verbatim (schemaV25's own doc comment: "the
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
