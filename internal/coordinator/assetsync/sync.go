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

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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

	// commandID, assetID, and filename identify the specific asset.fetch
	// this record tracks, beyond dispatchKey's own node/content-hash pair:
	// commandID correlates an inbound result-topic delivery back to this
	// record (see byCmdID and [Service.HandleMessage]); assetID and
	// filename are carried only so a recorded failure ([Service.
	// recordFetchFailure]) can name the asset without a second store
	// lookup.
	commandID string
	assetID   string
	filename  string
}

// FetchFailureRecord is the most recent asset.fetch failure [Service] has
// observed for one node/content-hash pair, from that node's own result-
// topic evidence; see [Service.LastFetchFailure].
type FetchFailureRecord struct {
	// Reason is the node's own ResultPayload.Reason: the full free-text
	// cause it computed, e.g. "asset.fetch: download failed: dial tcp
	// ...: connection refused". Never fabricated or summarized here.
	Reason string
	// FailedAt is when this coordinator process observed the failure
	// (this Service's own clock at the moment [Service.HandleMessage]
	// processed it): bookkeeping, not evidence of when the node's own
	// attempt happened, matching store.EventRecord.RecordedAt's identical
	// distinction from OccurredAt.
	FailedAt time.Time
	// AssetID and Filename name the asset that failed.
	AssetID  string
	Filename string
}

// Settings is this service's live, no-restart-able configuration — Track G
// seam G-4's assets.settings configuration kind (ADR-039), mirrored here so
// package assetsync does not need to import internal/coordinator/config for
// one struct shape (declared at the consumer, per this codebase's standing
// convention). SHOWMESH_ASSET_DIR has no field here — it stays
// environment-only (ADR-039 decision 2) and this package never reads it.
type Settings struct {
	// ContentBaseURL is SHOWMESH_ASSET_CONTENT_BASE_URL / assets.settings'
	// contentBaseUrl. Empty means this service dispatches nothing — see
	// [Service.Enabled].
	ContentBaseURL string
	// MaxUploadBytes bounds a single asset upload. This service does not
	// use it directly (the upload handler does); it is carried here only
	// because assets.settings is one configuration object and this Service
	// is that object's one live holder.
	MaxUploadBytes int64
	// SyncInterval is how often [Service.Run] recomputes every declared
	// node's gap, in addition to running on every [Service.Nudge].
	SyncInterval time.Duration
	// InventoryInterval is this coordinator's own copy of the agent's
	// inventory-report cadence — see [BuildNodeManifest]'s staleness
	// window.
	InventoryInterval time.Duration
}

// Service is ADR-028 decision 7's sync service: on upload (via [Service.
// Nudge]) and on its own tick interval — never in response to a show
// starting — it computes every declared node's gap against the active
// show ([BuildNodeManifest]) and dispatches one asset.fetch command per
// missing asset. It never deletes anything, on any node, for any reason.
//
// Track G seam G-4 (ADR-039 decision 6) made every field of [Settings]
// live: a coordinator operator can change contentBaseUrl, the upload
// limit, or either interval through the assets.settings configuration
// kind while this process runs, and [Service.Run] picks each one up
// without a restart — including the "zero to one and back to zero"
// transition ([Service.Enabled] flipping while Run is already looping).
//
// The zero value is not usable; construct with [NewService].
type Service struct {
	st     *store.Store
	pub    Publisher
	logger *slog.Logger
	now    func() time.Time

	settingsMu sync.RWMutex
	settings   Settings

	// nudge is buffered 1: a pending, not-yet-consumed nudge coalesces
	// with a second one rather than queuing, matching
	// internal/coordinator/collector's Runner.Nudge shape — a sync is
	// about to run anyway, and a second "run now" before the first has
	// even started adds nothing.
	nudge chan struct{}

	// requestMu guards pendingNodes: node ids queued by [Service.
	// RequestNode] for [Service.syncRequestedNodes] to drain on Run's next
	// iteration, syncing exactly those nodes rather than every declared
	// one. Never dropped, unlike nudge's own coalescing signal.
	requestMu    sync.Mutex
	pendingNodes map[string]bool

	mu       sync.Mutex
	inFlight map[dispatchKey]dispatchRecord

	// byCmdID correlates an inbound result-topic delivery's CommandID back
	// to the dispatchKey [Service.maybeDispatch] recorded it under, since
	// a result payload carries only CommandID/Action/Outcome/Reason/
	// Evidence, never the original dispatch's content hash. Guarded by mu,
	// like inFlight.
	byCmdID map[string]dispatchKey

	// failures is this Service's live view of the last known asset.fetch
	// failure per dispatchKey, consulted through [Service.
	// LastFetchFailure]. In-memory only, like inFlight; see that
	// method's own doc comment for why that is the honest ADR-011 answer
	// rather than a gap to close. Guarded by mu.
	failures map[dispatchKey]FetchFailureRecord
}

// NewService constructs a Service holding initial. See [Settings]' own
// field doc comments; an empty initial.ContentBaseURL means [Service.
// Enabled] starts false, which is a normal, deliberate state, not an error.
func NewService(st *store.Store, pub Publisher, logger *slog.Logger, initial Settings) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		st: st, pub: pub, logger: logger, now: time.Now,
		settings: initial,
		nudge:    make(chan struct{}, 1), inFlight: make(map[dispatchKey]dispatchRecord),
		byCmdID: make(map[string]dispatchKey), failures: make(map[dispatchKey]FetchFailureRecord),
	}
}

// SetSettings replaces this service's live settings, taking effect on
// [Service.Run]'s very next loop iteration — see that method's doc
// comment. Nudges Run to re-check promptly, rather than waiting out
// whatever of the OLD sync interval remains, whenever the new settings
// actually differ from the current ones.
func (s *Service) SetSettings(v Settings) {
	s.settingsMu.Lock()
	changed := s.settings != v
	s.settings = v
	s.settingsMu.Unlock()
	if changed {
		s.Nudge()
	}
}

// Settings returns this service's current live settings.
func (s *Service) Settings() Settings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

// ContentBaseURL returns the current live content base URL — satisfies
// api.AssetSettingsSource with no adapter, the same "narrow interface
// declared at the consumer" property [Service.Nudge] already gives
// api.AssetSyncNudger.
func (s *Service) ContentBaseURL() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings.ContentBaseURL
}

// MaxUploadBytes returns the current live upload limit.
func (s *Service) MaxUploadBytes() int64 {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings.MaxUploadBytes
}

// InventoryInterval returns the current live inventory-report cadence.
func (s *Service) InventoryInterval() time.Duration {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings.InventoryInterval
}

// syncInterval returns the current live sync-pass cadence, used only by
// [Service.Run]'s own loop.
func (s *Service) syncInterval() time.Duration {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings.SyncInterval
}

// WithClock overrides Service's clock. Production never calls this;
// exists so a test can drive dispatch expiry deterministically.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Enabled reports whether this service currently holds a non-empty
// content base URL. Read live on every [Service.Run] loop iteration, so
// this can flip in either direction while Run is already looping — see
// that method's doc comment.
func (s *Service) Enabled() bool {
	return s.ContentBaseURL() != ""
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

// RequestNode queues nodeID for [Service.syncRequestedNodes] to sync on
// Run's next iteration, then nudges Run to run promptly. Unlike Nudge's
// own coalescing signal, a queued node id is never dropped: an operator
// asking to re-sync one node must not be silently absorbed into whatever
// nudge happened to already be pending.
func (s *Service) RequestNode(nodeID string) {
	s.requestMu.Lock()
	if s.pendingNodes == nil {
		s.pendingNodes = make(map[string]bool)
	}
	s.pendingNodes[nodeID] = true
	s.requestMu.Unlock()
	s.Nudge()
}

// Run ticks immediately, then waits [Service.syncInterval] (read fresh on
// every iteration, or a [Service.Nudge], or ctx.Done) before ticking
// again, until ctx is cancelled. Track G seam G-4 (ADR-039 decision 6):
// unlike before, Run never returns early because [Service.Enabled] is
// false — it keeps looping and simply skips dispatch work on any
// iteration where Enabled reports false, because contentBaseUrl can be
// set (or cleared) through the assets.settings configuration kind at any
// point while this process runs, and a Run that already returned could
// never notice. Every enabled<->disabled transition is logged once, not
// on every iteration, matching the original "says so once" contract for
// each state rather than repeating it every tick.
func (s *Service) Run(ctx context.Context) {
	var loggedEnabled *bool

	for {
		enabled := s.Enabled()
		if loggedEnabled == nil || *loggedEnabled != enabled {
			if enabled {
				s.logger.Info("asset sync service is enabled: nodes can receive assets over the network")
			} else {
				s.logger.Warn("asset sync service is disabled: no contentBaseUrl is configured (assets.settings); " +
					"nodes will never receive an asset over the network, and the asset manifest states this as the reason a node cannot be confirmed ready")
			}
			loggedEnabled = &enabled
		}

		if enabled {
			s.tick(ctx)
			s.syncRequestedNodes(ctx)
		}
		if ctx.Err() != nil {
			return
		}

		timer := time.NewTimer(s.syncInterval())
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

// syncRequestedNodes drains every node id [Service.RequestNode] queued and
// syncs exactly those, independent of tick's own full-fleet pass. A store
// error or no active show skips this drain the same way tick does; the
// node ids stay dropped rather than requeued, matching tick's own "next
// pass tries again" posture for anything a caller cares to re-request.
func (s *Service) syncRequestedNodes(ctx context.Context) {
	s.requestMu.Lock()
	nodes := s.pendingNodes
	s.pendingNodes = nil
	s.requestMu.Unlock()
	if len(nodes) == 0 {
		return
	}

	active, err := ResolveActiveShow(ctx, s.st)
	if err != nil {
		s.logger.Error("asset sync: failed to resolve active show for a requested node", "error", err)
		return
	}
	if !active.Configured {
		return
	}
	for nodeID := range nodes {
		s.syncNode(ctx, active.ShowID, nodeID)
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
	m, err := buildNodeManifestForActiveShow(ctx, s.st, s.now(), s.InventoryInterval(), nodeID, ActiveShow{Configured: true, ShowID: showID})
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
			// byCmdID must not outlive its inFlight record: HandleMessage
			// reads inFlight[key] only while byCmdID still tracks the
			// commandID, and would otherwise record a failure carrying no
			// asset metadata.
			delete(s.byCmdID, rec.commandID)
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
			delete(s.byCmdID, rec.commandID)
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
	commandID := uuid.NewString()
	now := s.now()
	s.inFlight[key] = dispatchRecord{
		dispatchedAt: now, expiresAt: now.Add(inFlightExpiry(asset.SizeBytes)),
		commandID: commandID, assetID: asset.AssetID, filename: asset.Filename,
	}
	s.byCmdID[commandID] = key
	s.mu.Unlock()

	if err := s.dispatchFetch(ctx, nodeID, asset, commandID); err != nil {
		s.logger.Warn("asset sync: failed to dispatch asset.fetch", "node_id", nodeID, "asset_id", asset.AssetID, "error", err)
		s.mu.Lock()
		delete(s.inFlight, key)
		delete(s.byCmdID, commandID)
		s.mu.Unlock()
		return
	}
	s.logger.Info("asset sync: dispatched asset.fetch", "node_id", nodeID, "asset_id", asset.AssetID, "content_hash", asset.ContentHash, "size_bytes", asset.SizeBytes, "command_id", commandID)
}

// dispatchFetch publishes one asset.fetch [mqttproto.CmdPayload] to
// nodeID's cmd topic, QoS 1, never retained (mqttproto.CmdDeliveryPolicy).
// Params match §5.1 exactly: assetId, contentHash, filename, sizeBytes,
// url. sizeBytes is asset.SizeBytes verbatim — internal/agent's own
// parseAssetFetchParams refuses anything not at least 1, so a zero-size
// asset record (which [store.createAsset] already refuses to write) can
// never reach the agent as a value it would accept.
//
// commandID is generated by the caller ([Service.maybeDispatch]), not
// here, so it can be recorded in [Service.inFlight]/[Service.byCmdID]
// BEFORE the publish is attempted, the same "reserve, then attempt"
// order maybeDispatch already uses for its own concurrency budget, and
// so [Service.HandleMessage] can later correlate this exact command's
// result back to the dispatch that produced it.
func (s *Service) dispatchFetch(ctx context.Context, nodeID string, asset ExpectedAsset, commandID string) error {
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return fmt.Errorf("build cmd topic: %w", err)
	}

	// No CmdPayload.Deadline: syncNode recomputes what's missing fresh
	// from currently-desired vs. currently-held hashes every tick, so a
	// stale fetch is either still correct or inert, never harmful.
	payload := mqttproto.CmdPayload{
		CommandID:      commandID,
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
// the CURRENT live content base URL joined with the content route the
// (separately built) asset store's HTTP handler serves, GET
// /api/v1/assets/{id}/content.
func (s *Service) fetchURL(assetID string) string {
	return strings.TrimRight(s.ContentBaseURL(), "/") + "/api/v1/assets/" + assetID + "/content"
}

// --- consuming asset.fetch results ---
//
// The node already computes and publishes the exact reason an asset.fetch
// failed (internal/agent/command.go's failedResult, ResultPayload.Reason)
// to its own result topic. Before this, nothing on the coordinator ever
// subscribed to that topic family: the sync loop dispatched and moved on,
// and a dispatch that failed looked identical, forever, to one the sync
// service simply had not gotten to yet: a never-confirmed in-flight
// record just silently expired ([pruneExpiredInFlight]) and was retried
// next tick with no record anything went wrong. Subscriptions/HandleMessage
// below close that gap.
//
// Mechanism chosen: extend this Service's own subscription set, rather
// than reuse internal/coordinator/broker/response.go's per-request
// AwaitResponse/registerResponseWaiter machinery. That machinery is built
// for Step 9's macro mqtt action: issue one command, block the calling
// goroutine until a live matching reply or a deadline, then return.
// [Service.maybeDispatch] cannot adopt that shape without changing this
// package's whole dispatch cadence: a sync tick fires up to
// maxInFlightTotal asset.fetch commands across many nodes, fire-and-forget,
// and must never hold a goroutine (and the concurrency budget it
// represents) open waiting on any one of their results, especially not for
// a large-file transfer that can legitimately take up to
// assetstore.UploadBudget to complete. A subscription-based handler
// consumes whatever result arrives, whenever it arrives, with no per-
// dispatch goroutine or deadline of its own; retry/expiry stays exactly
// [pruneExpiredInFlight]'s job, unchanged.
//
// Retained-message and restart semantics: mqttproto.ResultDeliveryPolicy is
// Retain:false (a result is a point-in-time message, not durable state,
// see that policy's own doc comment), so there is no retained-replay
// staleness question here the way there is for broker.BrokerManager's own
// evidence-staleness rule (broker.go's evidenceStalenessWindow) or for
// hello/observed's retained topics: a coordinator that restarts learns
// nothing about a fetch dispatched before the restart, the same as
// [Service.inFlight] itself, which is also in-memory only and does not
// survive a restart. That is the honest ADR-011 answer (an unmeasured
// value is unknown, never a manufactured "still pending" or "no failure")
// rather than a gap worth closing with a persisted correlation table: the
// sync loop redispatches a never-confirmed asset on its own next tick
// regardless, so a restart costs at most one extra round trip before fresh
// evidence exists again. See [Service.HandleMessage]'s defensive RETAIN
// check below for why this is verified, not merely assumed.
func (s *Service) Subscriptions() []broker.Subscription {
	return []broker.Subscription{{Filter: mqttproto.SubscribeResult, QoS: 1}}
}

// HandleMessage consumes one inbound result-topic delivery; it is a
// broker.MessageHandler (see that type's own doc comment for what a
// handler must not block on). Every inbound message on
// mqttproto.SubscribeResult reaches this method, for every command every
// node executes, not only asset.fetch, so this is also the filter down
// to the ones this Service tracks.
func (s *Service) HandleMessage(msg broker.Message) {
	if msg.Retained {
		// See [Service.Subscriptions]'s own doc comment: a result topic is
		// never retained in this codebase today (mqttproto.
		// ResultDeliveryPolicy), so this is defence in depth against a
		// misbehaving publisher or a future policy change, matching
		// broker/response.go's identical RETAIN check on the same class of
		// topic.
		s.logger.Warn("asset sync: ignoring unexpectedly retained delivery on a result topic", "topic", msg.Topic)
		return
	}

	topic, err := mqttproto.ParseTopic(msg.Topic)
	if err != nil || topic.Kind != mqttproto.TopicKindResult {
		// mqttproto.SubscribeResult only ever delivers a well-formed
		// result topic; this is a defensive skip, not an expected path.
		return
	}

	env, err := mqttproto.DecodeEnvelope(msg.Payload)
	if err != nil {
		s.logger.Warn("asset sync: dropping malformed result envelope", "node_id", topic.NodeID, "error", err)
		return
	}
	if err := mqttproto.CheckNodeID(env, topic.NodeID); err != nil {
		s.logger.Warn("asset sync: dropping result envelope with a node ID mismatch", "node_id", topic.NodeID, "error", err)
		return
	}
	result, err := mqttproto.DecodeResultPayload(env)
	if err != nil {
		s.logger.Warn("asset sync: dropping malformed result payload", "node_id", topic.NodeID, "error", err)
		return
	}
	if result.Action != "asset.fetch" {
		// Some other command's result, sharing the same topic wildcard,
		// not this Service's concern.
		return
	}

	s.mu.Lock()
	key, tracked := s.byCmdID[result.CommandID]
	var rec dispatchRecord
	if tracked {
		rec = s.inFlight[key]
		// One-shot: delete on first delivery so a redelivered/replayed
		// result for the same command_id (internal/agent/command.go's own
		// idempotency-cache replay path) is silently ignored rather than
		// recorded, and re-logged as an event, a second time.
		delete(s.byCmdID, result.CommandID)
	}
	s.mu.Unlock()

	if !tracked {
		// Not a dispatch this Service instance is currently tracking:
		// already resolved by an earlier delivery, already pruned by
		// [pruneExpiredInFlight], or dispatched before a coordinator
		// restart (in-flight tracking is in-memory only; see
		// [Service.Subscriptions]'s own doc comment). Nothing to
		// correlate this result against.
		return
	}

	switch result.Outcome {
	case mqttproto.OutcomeConfirmed:
		// Corroborating evidence, on top of (not instead of) the
		// inventory-report-based confirmation [FetchConfirmed] already
		// performs: clear any stale failure record for this exact asset so
		// the manifest stops citing a cause that no longer applies.
		s.mu.Lock()
		delete(s.failures, key)
		s.mu.Unlock()
	case mqttproto.OutcomeFailed, mqttproto.OutcomeRefused, mqttproto.OutcomeUnconfirmed:
		// All three are a genuine non-success this Service did not
		// previously have any record of: OutcomeFailed is §5.1's "download
		// failed"/verification-mismatch case, OutcomeRefused is the agent
		// declining the command outright (deadline already elapsed, not on
		// its allowlist), and OutcomeUnconfirmed is "applied, but the
		// post-write read-back evidence did not match", never fabricated
		// as a plain success. result.Reason is the node's own free-text
		// cause in every one of these three; never synthesized here.
		s.recordFetchFailure(context.Background(), topic.NodeID, key.contentHash, result.Outcome, result.Reason, rec)
	}
}

// recordFetchFailure records key's failure both in this Service's own live
// view (consulted through [Service.LastFetchFailure]) and, durably, as a
// store.EventRecord so GET /api/v1/events shows it: reason, outcome comes
// straight from the node's own result, never fabricated or summarized.
// Best-effort: an append failure is logged rather than propagated, matching
// every other event producer in this codebase (internal/coordinator/
// inventory.Manager.observeLiveness/appendRenderEvent, internal/
// coordinator/macro/events.go): a lost event must never block or fail the
// caller that is merely trying to record what already happened.
func (s *Service) recordFetchFailure(ctx context.Context, nodeID, contentHash, outcome, reason string, rec dispatchRecord) {
	failedAt := s.now()
	key := dispatchKey{nodeID: nodeID, contentHash: contentHash}
	s.mu.Lock()
	s.failures[key] = FetchFailureRecord{Reason: reason, FailedAt: failedAt, AssetID: rec.assetID, Filename: rec.filename}
	s.mu.Unlock()

	s.logger.Warn("asset sync: asset.fetch did not succeed", "node_id", nodeID, "asset_id", rec.assetID, "filename", rec.filename, "content_hash", contentHash, "outcome", outcome, "reason", reason)

	detail := map[string]any{
		"nodeId":      nodeID,
		"assetId":     rec.assetID,
		"filename":    rec.filename,
		"contentHash": contentHash,
		"outcome":     outcome,
		"reason":      reason,
	}
	details, err := json.Marshal(detail)
	if err != nil {
		// Unreachable: every value above is a plain string. Recorded
		// rather than dropped, matching appendRenderEvent's identical
		// guard one package over.
		s.logger.Error("asset sync: failed to encode asset.fetch failure event details", "node_id", nodeID, "asset_id", rec.assetID, "error", err)
		details = nil
	}

	if _, err := s.st.AppendEvent(ctx, store.EventRecord{
		Source:   "asset-sync",
		Resource: observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Category: "asset_sync",
		Severity: "warning",
		Summary:  fmt.Sprintf("asset.fetch %s for node %s: %s (%s)", outcome, nodeID, rec.filename, reason),
		Details:  json.RawMessage(details),
	}); err != nil {
		s.logger.Error("asset sync: failed to append asset.fetch failure event", "node_id", nodeID, "asset_id", rec.assetID, "error", err)
	}
}

// LastFetchFailure reports the most recent asset.fetch failure this
// Service has observed for nodeID attempting contentHash, if any. Consumed
// by internal/coordinator/api's manifest rendering (assetmanifest.go) so a
// node whose fetch genuinely failed says so, rather than reading
// identically to "sync has not gotten to it yet". See [Service.
// Subscriptions]'s own doc comment for why this is in-memory,
// process-lifetime state rather than a persisted one: the durable record
// of what happened lives in the events history this same failure was
// already appended to ([Service.recordFetchFailure]), not here; a
// coordinator restart forgetting this live view is the honest ADR-011
// answer (unknown, not a fabricated "no failure"), not a gap to close.
func (s *Service) LastFetchFailure(nodeID, contentHash string) (reason string, failedAt time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, found := s.failures[dispatchKey{nodeID: nodeID, contentHash: contentHash}]
	if !found {
		return "", time.Time{}, false
	}
	return rec.Reason, rec.FailedAt, true
}
