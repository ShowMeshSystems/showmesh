package assetsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Publisher is the coordinator's own MQTT publish capability this package
// depends on, declared here at the consumer so this package does not need
// to import internal/coordinator/broker. *broker.BrokerManager already
// satisfies this — see internal/agent's identical Publisher interface for
// the same shape used on the other end of this same command path.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// assetSyncIssuerPrincipalID/Name identify THIS SERVICE as
// [mqttproto.CmdPayload]'s required Issuer, for a command this background
// service dispatches on its own initiative — never behind an operator's
// authenticated request. This is NOT the same kind of attribution ADR-024
// decision 11 governs for an audit entry (there is no audit entry for a
// sync dispatch, and this package writes none): Issuer is the wire
// envelope's own required "who is asking" field, which
// [mqttproto.CmdPayload.Validate] requires non-empty regardless of who or
// what issued the command, so a genuinely unattended dispatch needs some
// non-empty value here rather than none.
const (
	assetSyncIssuerPrincipalID   = "showmesh-asset-sync"
	assetSyncIssuerPrincipalName = "ShowMesh asset sync"
)

// assetFetchConfirmationMethod matches internal/agent/command.go's
// confirmationMethodEvidence by convention, the same independently-chosen
// literal every CmdPayload.ConfirmationMethod producer in this codebase
// keeps in step with pkg/command.ConfirmationEvidence's value — see
// mqttproto.CmdPayload.ConfirmationMethod's own doc comment.
const assetFetchConfirmationMethod = "evidence"

// dispatchKey identifies one outstanding asset.fetch attempt: one node,
// one content hash.
type dispatchKey struct {
	nodeID      string
	contentHash string
}

type dispatchRecord struct {
	dispatchedAt time.Time
	expiresAt    time.Time
}

// Service is ADR-028 decision 7's sync service: on upload (via [Service.
// Nudge]) and on its own tick interval — never in response to a show
// starting — it computes every declared node's gap against the active
// show ([BuildNodeManifest]) and dispatches one asset.fetch command per
// missing asset. It never deletes anything, on any node, for any reason.
//
// The zero value is not usable; construct with [NewService].
type Service struct {
	st     *store.Store
	pub    Publisher
	logger *slog.Logger
	now    func() time.Time

	// contentBaseURL is SHOWMESH_ASSET_CONTENT_BASE_URL. Empty means this
	// service does not run — see [Service.Enabled] and [Service.Run].
	contentBaseURL    string
	inventoryInterval time.Duration

	// nudge is buffered 1: a pending, not-yet-consumed nudge coalesces
	// with a second one rather than queuing, matching
	// internal/coordinator/collector's Runner.Nudge shape — a sync is
	// about to run anyway, and a second "run now" before the first has
	// even started adds nothing.
	nudge chan struct{}

	mu       sync.Mutex
	inFlight map[dispatchKey]dispatchRecord
}

// NewService constructs a Service. contentBaseURL and inventoryInterval
// are config.Config's AssetContentBaseURL and AssetInventoryInterval
// respectively; an empty contentBaseURL means [Service.Enabled] is false
// and [Service.Run] returns immediately after logging why.
func NewService(st *store.Store, pub Publisher, logger *slog.Logger, contentBaseURL string, inventoryInterval time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		st: st, pub: pub, logger: logger, now: time.Now,
		contentBaseURL: contentBaseURL, inventoryInterval: inventoryInterval,
		nudge: make(chan struct{}, 1), inFlight: make(map[dispatchKey]dispatchRecord),
	}
}

// WithClock overrides Service's clock. Production never calls this;
// exists so a test can drive dispatch expiry deterministically.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Enabled reports whether this service was configured with a non-empty
// content base URL. When false, [Service.Run] performs no work at all —
// see that method's doc comment, and config.Config.AssetContentBaseURL's
// own doc comment for why an unset base URL must say so rather than
// silently doing nothing.
func (s *Service) Enabled() bool {
	return s.contentBaseURL != ""
}

// Nudge requests an out-of-band sync pass as soon as [Service.Run]'s
// current (or next) tick returns, instead of waiting out its own
// interval. This is the upload handler's hook (§5.1: "runs on upload and
// on SHOWMESH_ASSET_SYNC_INTERVAL"). Safe to call concurrently, at any
// time, including before Run starts and after Enabled is false (a no-op
// either way).
func (s *Service) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Run ticks immediately, then waits interval (or a [Service.Nudge], or
// ctx.Done) before ticking again, until ctx is cancelled. If !s.Enabled(),
// Run logs once, stating that no asset ever reaches a node over the
// network with this service off, and returns without starting a loop —
// per §5.1: "the service does NOT run [and] says so once at startup",
// never a silent no-op.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		s.logger.Warn("asset sync service is disabled: SHOWMESH_ASSET_CONTENT_BASE_URL is not set; " +
			"nodes will never receive an asset over the network, and the asset manifest states this as the reason a node cannot be confirmed ready")
		return
	}

	for {
		s.tick(ctx)
		if ctx.Err() != nil {
			return
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.nudge:
			timer.Stop()
		}
	}
}

// tick runs one full sync pass: resolve the active show, and for every
// declared node, close its gap. Never fatal — a store error is logged and
// this pass is skipped; the next tick tries again.
func (s *Service) tick(ctx context.Context) {
	active, err := ResolveActiveShow(ctx, s.st)
	if err != nil {
		s.logger.Error("asset sync: failed to resolve active show", "error", err)
		return
	}
	if !active.Configured {
		// Nothing to sync — BuildNodeManifest's own UnknownCauseNoActiveShow
		// already states this to a reader; this service simply has no gap
		// to compute without one.
		return
	}

	nodes, err := s.st.ListNodeDeclarations(ctx)
	if err != nil {
		s.logger.Error("asset sync: failed to list node declarations", "error", err)
		return
	}

	s.pruneExpiredInFlight()

	for _, n := range nodes {
		s.syncNode(ctx, active.ShowID, n.NodeID)
	}
}

// syncNode closes one node's gap against showID: reconcile any outstanding
// dispatch against the node's raw report evidence, then dispatch a fetch
// for whatever [ComputeNodeManifest] — reached only through
// buildNodeManifestForActiveShow, per assetsync/doc.go's "ComputeNodeManifest
// is the ONLY function in this codebase permitted to decide whether a node
// is ready" — says is actually missing.
func (s *Service) syncNode(ctx context.Context, showID, nodeID string) {
	report, err := s.st.GetNodeAssetReport(ctx, nodeID)
	var reportPtr *store.NodeAssetReportRecord
	switch {
	case err == nil:
		reportPtr = &report
	case errors.Is(err, store.ErrNodeAssetReportNotFound):
		// Never reported: nothing to reconcile against — reconcileInFlight
		// below is a no-op for a nil report, exactly like ComputeNodeManifest's
		// own UnknownCauseNeverReported case.
	default:
		s.logger.Error("asset sync: failed to read node asset report", "node_id", nodeID, "error", err)
		return
	}

	var inventory []store.NodeAssetInventoryRecord
	if reportPtr != nil {
		inventory, err = s.st.GetNodeAssetInventory(ctx, nodeID)
		if err != nil {
			s.logger.Error("asset sync: failed to read node asset inventory", "node_id", nodeID, "error", err)
			return
		}
	}

	held := make(map[string]bool, len(inventory))
	for _, item := range inventory {
		held[item.ContentHash] = true
	}

	// reconcileInFlight's post-dispatch fence ([FetchConfirmed]) is
	// deliberately independent of ComputeNodeManifest's staleness window
	// below: it asks "did a report AFTER this dispatch confirm it", which is
	// well-defined even off a report ComputeNodeManifest would call stale or
	// incomplete — an old report can still post-date an old dispatch.
	s.reconcileInFlight(nodeID, reportPtr, held)

	// P4b: this used to re-derive "what's missing" from expected/held
	// directly, with no freshness or completeness check — exactly the
	// precedence.go defect this package's own doc comment warns against,
	// one file over. active is ALREADY the resolved active show (tick's own
	// job); buildNodeManifestForActiveShow is the same function BuildNodeManifest
	// calls, so this dispatches against the identical verdict the manifest
	// API renders, never a second opinion.
	m, err := buildNodeManifestForActiveShow(ctx, s.st, s.now(), s.inventoryInterval, nodeID, ActiveShow{Configured: true, ShowID: showID})
	if err != nil {
		s.logger.Error("asset sync: failed to build node manifest", "node_id", nodeID, "error", err)
		return
	}
	if m.State != ManifestNotReady {
		// Ready: nothing to dispatch. Unknown (never reported, stale, or the
		// report itself said complete:false): you cannot know what is
		// missing from a node you cannot read, so this dispatches nothing —
		// the other half of P4's fix.
		return
	}
	for _, missing := range m.Missing {
		s.maybeDispatch(ctx, nodeID, ExpectedAsset{
			AssetID: missing.AssetID, SequenceID: missing.SequenceID,
			ContentHash: missing.ContentHash, Filename: missing.Filename, SizeBytes: missing.SizeBytes,
		})
	}
}

// FetchConfirmed reports whether a dispatched asset.fetch for contentHash
// on nodeID is confirmed complete: report must be non-nil, held must be
// true (the node's current inventory includes contentHash), AND report's
// own ReportedAt must be AT OR AFTER dispatchedAt.
//
// That third condition is the load-bearing one. Held-and-non-nil alone is
// NOT evidence this particular dispatch succeeded: a report from BEFORE
// dispatchedAt that already lists contentHash (a coincidentally
// pre-existing file — content-addressed dedup, ADR-028's own named
// optimization, makes this reachable — or a stale value read back by a
// misbehaving caller) proves nothing about what THIS command did. This is
// ADR-003's rule applied to a file transfer instead of a playback state:
// Step 7 measured a command reporting "confirmed" 179 microseconds after
// its own dispatch, off a reading collected before the command could
// possibly have taken effect. Removing the ReportedAt comparison here
// reproduces exactly that defect for asset sync.
func FetchConfirmed(dispatchedAt time.Time, report *store.NodeAssetReportRecord, held bool) bool {
	if report == nil || !held {
		return false
	}
	return !report.ReportedAt.Before(dispatchedAt)
}

// reconcileInFlight clears every in-flight record for nodeID that
// [FetchConfirmed] now says is done, using report and held (nodeID's
// current inventory, already reduced to a hash set by the caller).
func (s *Service) reconcileInFlight(nodeID string, report *store.NodeAssetReportRecord, held map[string]bool) {
	if report == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, rec := range s.inFlight {
		if key.nodeID != nodeID {
			continue
		}
		if FetchConfirmed(rec.dispatchedAt, report, held[key.contentHash]) {
			delete(s.inFlight, key)
		}
	}
}

// pruneExpiredInFlight drops any in-flight record whose [inFlightExpiry]
// has passed without confirmation, so an attempt that silently failed
// (store unreachable, verification failed, the agent never received the
// command) becomes eligible for a fresh dispatch on a later tick rather
// than being permanently wedged.
func (s *Service) pruneExpiredInFlight() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, rec := range s.inFlight {
		if !now.Before(rec.expiresAt) {
			delete(s.inFlight, key)
		}
	}
}

func (s *Service) countForNodeLocked(nodeID string) int {
	n := 0
	for key := range s.inFlight {
		if key.nodeID == nodeID {
			n++
		}
	}
	return n
}

// maybeDispatch dispatches one asset.fetch for asset on nodeID, subject to
// the concurrency budget (§5.1: at most [maxInFlightPerNode] per node, at
// most [maxInFlightTotal] overall) and to not already being in flight. A
// suppressed dispatch is not an error — the next tick tries again.
func (s *Service) maybeDispatch(ctx context.Context, nodeID string, asset ExpectedAsset) {
	key := dispatchKey{nodeID: nodeID, contentHash: asset.ContentHash}

	s.mu.Lock()
	if _, exists := s.inFlight[key]; exists {
		s.mu.Unlock()
		return
	}
	if s.countForNodeLocked(nodeID) >= maxInFlightPerNode || len(s.inFlight) >= maxInFlightTotal {
		s.mu.Unlock()
		return
	}
	now := s.now()
	s.inFlight[key] = dispatchRecord{dispatchedAt: now, expiresAt: now.Add(inFlightExpiry(asset.SizeBytes))}
	s.mu.Unlock()

	if err := s.dispatchFetch(ctx, nodeID, asset); err != nil {
		s.logger.Warn("asset sync: failed to dispatch asset.fetch", "node_id", nodeID, "asset_id", asset.AssetID, "error", err)
		s.mu.Lock()
		delete(s.inFlight, key)
		s.mu.Unlock()
		return
	}
	s.logger.Info("asset sync: dispatched asset.fetch", "node_id", nodeID, "asset_id", asset.AssetID, "content_hash", asset.ContentHash, "size_bytes", asset.SizeBytes)
}

// dispatchFetch publishes one asset.fetch [mqttproto.CmdPayload] to
// nodeID's cmd topic, QoS 1, never retained (mqttproto.CmdDeliveryPolicy).
// Params match §5.1 exactly: assetId, contentHash, filename, sizeBytes,
// url. sizeBytes is asset.SizeBytes verbatim — internal/agent's own
// parseAssetFetchParams refuses anything not at least 1, so a zero-size
// asset record (which [store.createAsset] already refuses to write) can
// never reach the agent as a value it would accept.
func (s *Service) dispatchFetch(ctx context.Context, nodeID string, asset ExpectedAsset) error {
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return fmt.Errorf("build cmd topic: %w", err)
	}

	payload := mqttproto.CmdPayload{
		CommandID:      uuid.NewString(),
		IdempotencyKey: uuid.NewString(),
		Action:         "asset.fetch",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"assetId":     asset.AssetID,
			"contentHash": asset.ContentHash,
			"filename":    asset.Filename,
			"sizeBytes":   asset.SizeBytes,
			"url":         s.fetchURL(asset.AssetID),
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: assetSyncIssuerPrincipalID, PrincipalName: assetSyncIssuerPrincipalName},
		ConfirmationMethod: assetFetchConfirmationMethod,
	}

	env, err := mqttproto.NewCmdEnvelope(s.now, nodeID, payload)
	if err != nil {
		return fmt.Errorf("build cmd envelope: %w", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal cmd envelope: %w", err)
	}
	return s.pub.Publish(ctx, topic, mqttproto.CmdDeliveryPolicy.QoS, mqttproto.CmdDeliveryPolicy.Retain, raw)
}

// fetchURL builds the URL an agent's asset.fetch operation downloads from:
// s.contentBaseURL joined with the content route the (separately built)
// asset store's HTTP handler serves, GET /api/v1/assets/{id}/content.
func (s *Service) fetchURL(assetID string) string {
	return strings.TrimRight(s.contentBaseURL, "/") + "/api/v1/assets/" + assetID + "/content"
}
