package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/fallbackactivation"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// The fallback ingress (ADR-048 decision 3): one route takes a
// coordinator-signed fallback program, the other an activation signed by
// the FPP player that program names. Neither accepts anything else.

const (
	fallbackActivationsPerMinute = 120
	fallbackProgramsPerMinute    = 30

	fallbackRouteProgram    = "program"
	fallbackRouteActivation = "activation"
)

// fallbackIngress serves both routes. A nil coordinatorKey means this
// node pinned no coordinator key, and both routes refuse.
type fallbackIngress struct {
	nodeID         string
	coordinatorKey ed25519.PublicKey
	programs       *fallbackProgramStore
	fence          *fallbackFence
	activations    *fallbackRateLimiter
	deliveries     *fallbackRateLimiter
	decisions      *fallbackDecisionLog
	now            func() time.Time
	logger         *slog.Logger

	mu       sync.Mutex
	activate OperationFunc
}

// newFallbackIngress loads this node's held programs and replay fence
// from assetDir. A fence that cannot be opened leaves the program route
// working and makes every activation refuse.
func newFallbackIngress(nodeID, assetDir string, coordinatorKey ed25519.PublicKey, now func() time.Time, logger *slog.Logger) *fallbackIngress {
	fence, err := openFallbackFence(assetDir, now())
	if err != nil {
		logger.Warn("could not open this node's record of handled fallback requests; it will refuse every fallback activation until this is fixed", "error", err)
		fence = nil
	}
	return &fallbackIngress{
		nodeID:         nodeID,
		coordinatorKey: coordinatorKey,
		programs:       newFallbackProgramStore(assetDir, coordinatorKey, logger),
		fence:          fence,
		activations:    newFallbackRateLimiter(fallbackActivationsPerMinute),
		deliveries:     newFallbackRateLimiter(fallbackProgramsPerMinute),
		decisions:      newFallbackDecisionLog(),
		now:            now,
		logger:         logger,
	}
}

// setActivate hands the ingress this node's own "cue.activate" operation,
// the same function a coordinator dispatch runs.
func (g *fallbackIngress) setActivate(op OperationFunc) {
	g.mu.Lock()
	g.activate = op
	g.mu.Unlock()
}

// wrap serves the two fallback routes ahead of next, so they answer
// whether or not the FPP Connect upload surface is enabled.
func (g *fallbackIngress) wrap(next http.Handler) http.Handler {
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodPost && path == fallbackactivation.ActivationPath:
			g.handleFallbackActivation(w, r)
		case r.Method == http.MethodPut && strings.HasPrefix(path, fallbackactivation.ProgramPathPrefix):
			g.handleFallbackProgram(w, r, r.URL.Path[len(fallbackactivation.ProgramPathPrefix):])
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// fallbackAnswer is one response: the status, the outcome word, and what
// the decision record should say about the request.
type fallbackAnswer struct {
	status   int
	outcome  fallbackactivation.Outcome
	extra    map[string]any
	decision mqttproto.FallbackDecision
}

var fallbackOutcomeStatus = map[fallbackactivation.Outcome]int{
	fallbackactivation.OutcomeInstalled:               http.StatusOK,
	fallbackactivation.OutcomeAuthorized:              http.StatusOK,
	fallbackactivation.OutcomeMalformedRequest:        http.StatusBadRequest,
	fallbackactivation.OutcomeTooLarge:                http.StatusRequestEntityTooLarge,
	fallbackactivation.OutcomeRateLimited:             http.StatusTooManyRequests,
	fallbackactivation.OutcomeNoCoordinatorKey:        http.StatusServiceUnavailable,
	fallbackactivation.OutcomeStorageUnavailable:      http.StatusServiceUnavailable,
	fallbackactivation.OutcomeNotReady:                http.StatusServiceUnavailable,
	fallbackactivation.OutcomeProgramSignatureInvalid: http.StatusForbidden,
	fallbackactivation.OutcomeWrongFPPHost:            http.StatusForbidden,
	fallbackactivation.OutcomeWrongTarget:             http.StatusForbidden,
	fallbackactivation.OutcomeExecutorNotEnrolled:     http.StatusForbidden,
	fallbackactivation.OutcomeSignatureInvalid:        http.StatusForbidden,
	fallbackactivation.OutcomeCueNotAuthorized:        http.StatusForbidden,
}

// fallbackOutcomeReason is the one sentence an operator reads for each
// outcome. Any outcome not listed gets fallbackReasonNotStarted.
var fallbackOutcomeReason = map[fallbackactivation.Outcome]string{
	fallbackactivation.OutcomeInstalled:                          "This node holds the fallback program from this FPP player.",
	fallbackactivation.OutcomeAuthorized:                         "The Cue was started from the FPP player's fallback program.",
	fallbackactivation.OutcomeMalformedRequest:                   "A fallback request was not in the expected form. Update the FPP plugin.",
	fallbackactivation.OutcomeTooLarge:                           "A fallback request was too large. Update the FPP plugin.",
	fallbackactivation.OutcomeRateLimited:                        "This node received too many fallback requests from one address. The extra requests were refused.",
	fallbackactivation.OutcomeNoCoordinatorKey:                   "This node has no coordinator key, so it cannot check a fallback program. Enroll this node again.",
	fallbackactivation.OutcomeStorageUnavailable:                 "This node could not save what a fallback request needs, so nothing was started. Check this node's disk.",
	fallbackactivation.OutcomeNotReady:                           "This node is still starting. The plugin tries again.",
	fallbackactivation.OutcomeProgramSignatureInvalid:            "A fallback program was not signed by this node's coordinator. Check that the FPP player and this node use the same coordinator.",
	fallbackactivation.OutcomeProgramUnsupported:                 "A fallback program is in a form this node does not understand. Update this node.",
	fallbackactivation.OutcomeWrongFPPHost:                       "A fallback program was sent under a different FPP player than it was built for. Check the FPP plugin.",
	fallbackactivation.OutcomeWrongTarget:                        "A fallback request was meant for a different node. Nothing was started here.",
	fallbackactivation.OutcomeProgramExpired:                     "The fallback program from this FPP player has expired. It is renewed when the coordinator is back.",
	fallbackactivation.OutcomeProgramSuperseded:                  "This node already holds a newer fallback program from this FPP player. The older one was ignored.",
	fallbackactivation.OutcomeProgramNotInstalled:                "This node holds no fallback program from this FPP player. The plugin sends it and tries again.",
	fallbackactivation.OutcomeProgramNotCurrent:                  "The FPP player used a different fallback program than this node holds. The plugin sends its program and tries again.",
	fallbackactivation.OutcomeExecutorNotEnrolled:                "The fallback program names no key for this FPP player. Start the FPP plugin so it registers with the coordinator.",
	fallbackactivation.OutcomeSignatureInvalid:                   "A fallback request was not signed by the paired FPP player. Nothing was started.",
	fallbackactivation.OutcomeUnknownEntry:                       "The FPP player is playing an item the fallback program does not list. Nothing was started.",
	fallbackactivation.OutcomeCueNotAuthorized:                   "A fallback request asked for a Cue the fallback program does not allow for this item. Nothing was started.",
	fallbackactivation.OutcomeStaleGeneration:                    "A fallback request does not match the show this node has active. Nothing was started.",
	fallbackactivation.OutcomeStaleCatalog:                       "A fallback request does not match the Cue list this node holds. Nothing was started.",
	fallbackactivation.OutcomeReplayedExecution:                  "This fallback request was already handled. It was not run again.",
	fallbackactivation.OutcomeApplyFailed:                        "The Cue was allowed, but an output could not be started.",
	fallbackactivation.Outcome(cueauth.OutcomeCrossShow):         "A fallback request was for a different show than this node has active. Nothing was started.",
	fallbackactivation.Outcome(cueauth.OutcomeUnknownGeneration): "A fallback request was for a newer activation of the show than this node has. Nothing was started.",
	fallbackactivation.Outcome(cueauth.OutcomeUnknownCue):        "A fallback request asked for a Cue this node does not hold. Nothing was started.",
	fallbackactivation.Outcome(cueauth.OutcomeStaleCue):          "A fallback request asked for an older version of a Cue than this node holds. Nothing was started.",
	fallbackactivation.Outcome(cueauth.OutcomeAssetMissing):      "A file this Cue needs is missing from this node or does not match. Nothing was started.",
	"weather-delay-active":                                       weatherDelayActiveReason,
}

const fallbackReasonNotStarted = "The fallback request was refused. Nothing was started."

func (g *fallbackIngress) answer(outcome fallbackactivation.Outcome) fallbackAnswer {
	status, ok := fallbackOutcomeStatus[outcome]
	if !ok {
		status = http.StatusConflict
	}
	return fallbackAnswer{status: status, outcome: outcome}
}

// respond writes a, records it, and logs it. Every answer on either route
// goes through here, so no refusal can go unreported.
func (g *fallbackIngress) respond(w http.ResponseWriter, route string, a fallbackAnswer) {
	accepted := a.outcome == fallbackactivation.OutcomeInstalled || a.outcome == fallbackactivation.OutcomeAuthorized
	reason, ok := fallbackOutcomeReason[a.outcome]
	if !ok {
		reason = fallbackReasonNotStarted
	}

	decision := a.decision
	decision.At, decision.Route, decision.Accepted = g.now(), route, accepted
	decision.Outcome, decision.Reason = string(a.outcome), reason
	g.decisions.record(decision)

	level := slog.LevelWarn
	if accepted {
		level = slog.LevelInfo
	}
	g.logger.Log(context.Background(), level, "fallback "+route+" answered",
		"outcome", string(a.outcome), "fpp_instance_uuid", decision.FPPInstanceUUID,
		"execution_id", decision.ExecutionID, "entry_key", decision.EntryKey, "cue_id", decision.CueID)

	body := map[string]any{"accepted": accepted, "outcome": string(a.outcome), "reason": reason}
	if decision.ExecutionID != "" {
		body["executionId"] = decision.ExecutionID
	}
	for k, v := range a.extra {
		body[k] = v
	}
	fppConnectWriteJSON(w, a.status, body)
}

func fallbackClientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// readFallbackBody reads at most limit bytes. ok is false with the answer
// already chosen when the body is unreadable or over the limit.
func (g *fallbackIngress) readFallbackBody(r *http.Request, limit int64) ([]byte, fallbackAnswer, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, g.answer(fallbackactivation.OutcomeMalformedRequest), false
	}
	if int64(len(body)) > limit {
		return nil, g.answer(fallbackactivation.OutcomeTooLarge), false
	}
	return body, fallbackAnswer{}, true
}

// handleFallbackProgram is PUT /showmesh/v1/fallback/programs/{fppInstanceUuid}.
func (g *fallbackIngress) handleFallbackProgram(w http.ResponseWriter, r *http.Request, fppInstanceUUID string) {
	fppConnectSetReadDeadline(w, fppConnectDiscoveryReadDeadline, g.logger)
	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, g.logger)
	g.respond(w, fallbackRouteProgram, g.decideProgram(r, fppInstanceUUID))
}

func (g *fallbackIngress) decideProgram(r *http.Request, fppInstanceUUID string) fallbackAnswer {
	now := g.now()
	if !g.deliveries.allow(fallbackClientAddr(r), now) {
		return g.answer(fallbackactivation.OutcomeRateLimited)
	}
	withHost := func(a fallbackAnswer) fallbackAnswer {
		a.decision.FPPInstanceUUID = fppInstanceUUID
		return a
	}
	document, refusal, ok := g.readFallbackBody(r, fallbackactivation.MaxProgramBodyBytes)
	if !ok {
		return withHost(refusal)
	}
	if fppInstanceUUID == "" || !json.Valid(document) {
		return withHost(g.answer(fallbackactivation.OutcomeMalformedRequest))
	}
	if g.coordinatorKey == nil {
		return withHost(g.answer(fallbackactivation.OutcomeNoCoordinatorKey))
	}

	program, err := fallbackprogram.VerifyDocument(document, g.coordinatorKey)
	switch {
	case errors.Is(err, coordsig.ErrSignatureInvalid), errors.Is(err, coordsig.ErrSignatureSize):
		return withHost(g.answer(fallbackactivation.OutcomeProgramSignatureInvalid))
	case err != nil:
		return withHost(g.answer(fallbackactivation.OutcomeMalformedRequest))
	case program.SchemaVersion != fallbackprogram.SchemaVersion:
		return withHost(g.answer(fallbackactivation.OutcomeProgramUnsupported))
	case program.FPPInstanceUUID != fppInstanceUUID:
		return withHost(g.answer(fallbackactivation.OutcomeWrongFPPHost))
	case !fallbackProgramTargets(program, g.nodeID):
		return withHost(g.answer(fallbackactivation.OutcomeWrongTarget))
	case !now.Before(program.ExpiresAt):
		return withHost(g.answer(fallbackactivation.OutcomeProgramExpired))
	}
	err = g.programs.install(program, document, now)
	if errors.Is(err, errFallbackProgramSuperseded) {
		return withHost(g.answer(fallbackactivation.OutcomeProgramSuperseded))
	}
	if err != nil {
		g.logger.Warn("could not store a verified fallback program", "fpp_instance_uuid", fppInstanceUUID, "error", err)
		return withHost(g.answer(fallbackactivation.OutcomeStorageUnavailable))
	}

	a := withHost(g.answer(fallbackactivation.OutcomeInstalled))
	a.extra = map[string]any{
		"packageId": program.PackageID, "revision": program.Revision,
		"expiresAt": program.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	return a
}

func fallbackProgramTargets(program fallbackprogram.Program, nodeID string) bool {
	for _, entry := range program.Entries {
		for _, target := range entry.Targets {
			if target.NodeID == nodeID {
				return true
			}
		}
	}
	return false
}

// handleFallbackActivation is POST /showmesh/v1/fallback/activations.
func (g *fallbackIngress) handleFallbackActivation(w http.ResponseWriter, r *http.Request) {
	fppConnectSetReadDeadline(w, fppConnectDiscoveryReadDeadline, g.logger)
	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, g.logger)
	// Detached so a caller that hangs up cannot cancel a Cue mid-start.
	g.respond(w, fallbackRouteActivation, g.decideActivation(context.WithoutCancel(r.Context()), r))
}

// decideActivation runs the checks of contract section 5.6 in its order.
// Only a request that passes every one reaches the Cue activation.
func (g *fallbackIngress) decideActivation(ctx context.Context, r *http.Request) fallbackAnswer {
	now := g.now()
	if !g.activations.allow(fallbackClientAddr(r), now) {
		return g.answer(fallbackactivation.OutcomeRateLimited)
	}
	body, refusal, ok := g.readFallbackBody(r, fallbackactivation.MaxActivationBodyBytes)
	if !ok {
		return refusal
	}
	received, err := fallbackactivation.Decode(body)
	if err != nil {
		return g.answer(fallbackactivation.OutcomeMalformedRequest)
	}
	req := received.Request
	refuse := func(outcome fallbackactivation.Outcome) fallbackAnswer {
		a := g.answer(outcome)
		a.decision = mqttproto.FallbackDecision{
			FPPInstanceUUID: req.FPPInstanceUUID, ExecutionID: req.ExecutionID, EntryKey: req.EntryKey, CueID: req.CueID,
		}
		return a
	}

	if req.NodeID != g.nodeID {
		return refuse(fallbackactivation.OutcomeWrongTarget)
	}
	if g.coordinatorKey == nil {
		return refuse(fallbackactivation.OutcomeNoCoordinatorKey)
	}
	held, ok := g.programs.get(req.FPPInstanceUUID)
	if !ok {
		return refuse(fallbackactivation.OutcomeProgramNotInstalled)
	}
	if held.executorKey == nil {
		return refuse(fallbackactivation.OutcomeExecutorNotEnrolled)
	}
	if received.Verify(held.executorKey) != nil {
		return refuse(fallbackactivation.OutcomeSignatureInvalid)
	}
	program := held.program
	if req.PackageID != program.PackageID || req.PackageRevision != program.Revision || !req.ProgramExpiresAt.Equal(program.ExpiresAt) {
		return refuse(fallbackactivation.OutcomeProgramNotCurrent)
	}
	if !now.Before(program.ExpiresAt) {
		return refuse(fallbackactivation.OutcomeProgramExpired)
	}
	entry, ok := fallbackProgramEntry(program, req.EntryKey)
	if !ok {
		return refuse(fallbackactivation.OutcomeUnknownEntry)
	}
	if req.CueID != entry.CueID || req.CueRevision != entry.CueRevision {
		return refuse(fallbackactivation.OutcomeCueNotAuthorized)
	}
	if !fallbackEntryTargets(entry, g.nodeID) {
		return refuse(fallbackactivation.OutcomeWrongTarget)
	}
	if req.Generation != program.Generation {
		return refuse(fallbackactivation.OutcomeStaleGeneration)
	}
	if req.CatalogRevision != program.CatalogRevisions[g.nodeID] {
		return refuse(fallbackactivation.OutcomeStaleCatalog)
	}

	g.mu.Lock()
	activate := g.activate
	g.mu.Unlock()
	if activate == nil {
		return refuse(fallbackactivation.OutcomeNotReady)
	}
	if g.fence == nil {
		return refuse(fallbackactivation.OutcomeStorageUnavailable)
	}
	firstOutcome, replayed, err := g.fence.claim(req.ExecutionID, program.ExpiresAt)
	if err != nil {
		g.logger.Warn("could not record a fallback request before running it; it was not run", "execution_id", req.ExecutionID, "error", err)
		return refuse(fallbackactivation.OutcomeStorageUnavailable)
	}
	if replayed {
		a := refuse(fallbackactivation.OutcomeReplayedExecution)
		a.extra = map[string]any{"firstOutcome": firstOutcome}
		return a
	}

	outcome, reasons := g.runCueActivation(ctx, activate, req, program.Show, now)
	if err := g.fence.finish(req.ExecutionID, string(outcome)); err != nil {
		g.logger.Warn("could not record the result of a fallback request; a repeat of it will report an unknown first result", "execution_id", req.ExecutionID, "error", err)
	}
	a := refuse(outcome)
	if len(reasons) > 0 {
		a.extra = map[string]any{"reasons": reasons}
	}
	return a
}

func fallbackProgramEntry(program fallbackprogram.Program, entryKey string) (fallbackprogram.EntryMapping, bool) {
	for _, entry := range program.Entries {
		if entry.EntryKey == entryKey {
			return entry, true
		}
	}
	return fallbackprogram.EntryMapping{}, false
}

func fallbackEntryTargets(entry fallbackprogram.EntryMapping, nodeID string) bool {
	for _, target := range entry.Targets {
		if target.NodeID == nodeID {
			return true
		}
	}
	return false
}

// runCueActivation hands the request to this node's own "cue.activate" as
// the envelope a coordinator dispatch carries. What the Cue does is read
// from the catalog this node holds, never from the request.
func (g *fallbackIngress) runCueActivation(ctx context.Context, activate OperationFunc, req fallbackactivation.Request, show string, now time.Time) (fallbackactivation.Outcome, []string) {
	act := cueactivation.Activation{
		Runner: fallbackactivation.Runner, RunnerInstance: req.FPPInstanceUUID, ActivationID: req.ExecutionID,
		Show: show, Generation: req.Generation, CatalogRevision: req.CatalogRevision,
		CueID: req.CueID, CueRevision: req.CueRevision, EvidenceAt: now,
	}
	raw, err := json.Marshal(act)
	var params map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &params)
	}
	if err != nil {
		return fallbackactivation.OutcomeApplyFailed, []string{"This node could not build the Cue request."}
	}
	result, err := activate(ctx, params, g.now)
	if err != nil {
		g.logger.Warn("the Cue activation failed for a fallback request", "execution_id", req.ExecutionID, "error", err)
		return fallbackactivation.OutcomeApplyFailed, []string{"This node could not run the Cue."}
	}
	value, _ := result.Value.(map[string]any)
	outcome, _ := value["outcome"].(string)
	if outcome == "" {
		return fallbackactivation.OutcomeApplyFailed, []string{"This node could not run the Cue."}
	}
	reasons, _ := value["reasons"].([]string)
	return fallbackactivation.Outcome(outcome), reasons
}
