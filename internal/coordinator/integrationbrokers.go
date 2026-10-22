package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/enrollment"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const (
	integrationBrokerReconcileInterval = 10 * time.Second
	// replacedIntegrationBrokerGrace outlives the longest response wait, so an
	// action already waiting on a replaced connection still gets its answer.
	replacedIntegrationBrokerGrace     = 130 * time.Second
	integrationBrokerDisconnectTimeout = 5 * time.Second
)

// disconnectIntegrationBroker bounds a disconnect, which otherwise waits
// forever on a connection that never came up.
func disconnectIntegrationBroker(bm *broker.BrokerManager) error {
	ctx, cancel := context.WithTimeout(context.Background(), integrationBrokerDisconnectTimeout)
	defer cancel()
	return bm.Disconnect(ctx)
}

// integrationBrokerStore is the slice of *store.Store the broker set reads.
type integrationBrokerStore interface {
	ListConfigObjects(ctx context.Context, kind string) ([]store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
	GetCredential(ctx context.Context, kind, objectID, field string) (string, bool, error)
}

type runningIntegrationBroker struct {
	cfg config.IntegrationBroker
	bm  *broker.BrokerManager
}

// integrationBrokerSet keeps registry connected to the brokers an mqtt action
// may name: the built-in broker, when available, plus every stored
// integration.broker, or the unmigrated environment list when migration failed.
type integrationBrokerSet struct {
	st           integrationBrokerStore
	registry     *broker.Registry
	clientIDBase string
	builtIn      *config.IntegrationBroker
	envFallback  []config.IntegrationBroker
	logger       *slog.Logger
	connect      func(ctx context.Context, b config.IntegrationBroker) (*broker.BrokerManager, error)

	mu      sync.Mutex
	running map[string]runningIntegrationBroker
	wake    chan struct{}
}

func newIntegrationBrokerSet(st integrationBrokerStore, clientIDBase string, builtIn *config.IntegrationBroker, envFallback []config.IntegrationBroker, logger *slog.Logger) *integrationBrokerSet {
	s := &integrationBrokerSet{
		st: st, registry: broker.NewRegistry(), clientIDBase: clientIDBase,
		builtIn: builtIn, envFallback: envFallback, logger: logger,
		running: map[string]runningIntegrationBroker{}, wake: make(chan struct{}, 1),
	}
	s.connect = s.connectBroker
	return s
}

func (s *integrationBrokerSet) connectBroker(ctx context.Context, b config.IntegrationBroker) (*broker.BrokerManager, error) {
	return broker.NewBrokerManager(ctx, config.Config{
		MQTTBroker:   b.URL,
		MQTTClientID: s.clientIDBase + "-integration-" + b.ID,
		MQTTUsername: b.Username,
		MQTTPassword: b.Password,
	}, s.logger, nil, nil, nil)
}

// BuiltIn returns the built-in broker entry, when this coordinator manages the built-in broker's logins.
func (s *integrationBrokerSet) BuiltIn() (config.IntegrationBroker, bool) {
	if s.builtIn == nil {
		return config.IntegrationBroker{}, false
	}
	return *s.builtIn, true
}

// IntegrationBrokers returns every broker an action may name right now, built-in first.
func (s *integrationBrokerSet) IntegrationBrokers(ctx context.Context) ([]config.IntegrationBroker, error) {
	var out []config.IntegrationBroker
	if s.builtIn != nil {
		out = append(out, *s.builtIn)
	}
	if s.envFallback != nil {
		return append(out, s.envFallback...), nil
	}
	stored, err := readStoredIntegrationBrokers(ctx, s.st)
	if err != nil {
		return nil, err
	}
	return append(out, stored...), nil
}

// Notify asks for a reconcile now instead of at the next tick.
func (s *integrationBrokerSet) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run reconciles once immediately, then on every tick or Notify until ctx ends.
func (s *integrationBrokerSet) Run(ctx context.Context) {
	ticker := time.NewTicker(integrationBrokerReconcileInterval)
	defer ticker.Stop()
	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// reconcile connects added or changed brokers and drops removed ones. A read
// failure leaves every running connection in place.
func (s *integrationBrokerSet) reconcile(ctx context.Context) {
	desired, err := s.IntegrationBrokers(ctx)
	if err != nil {
		s.logger.Warn("could not read the integration brokers; keeping the current connections", "error", err)
		return
	}
	want := make(map[string]config.IntegrationBroker, len(desired))
	for _, b := range desired {
		want[b.ID] = b
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cur := range s.running {
		if b, ok := want[id]; ok && b == cur.cfg {
			continue
		}
		s.registry.Unregister(id)
		delete(s.running, id)
		retire := cur.bm
		time.AfterFunc(replacedIntegrationBrokerGrace, func() { _ = disconnectIntegrationBroker(retire) })
		s.logger.Info("integration broker removed or changed; its old connection closes shortly", "broker", id)
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := s.running[id]; ok {
			continue
		}
		b := want[id]
		bm, err := s.connect(ctx, b)
		if err != nil {
			s.logger.Warn("could not set up the integration broker connection; retrying at the next check", "broker", id, "error", err)
			continue
		}
		if err := s.registry.Register(id, bm); err != nil {
			_ = disconnectIntegrationBroker(bm)
			s.logger.Warn("could not register the integration broker", "broker", id, "error", err)
			continue
		}
		s.running[id] = runningIntegrationBroker{cfg: b, bm: bm}
		s.logger.Info("integration broker connected", "broker", id)
	}
}

// Close disconnects every running connection.
func (s *integrationBrokerSet) Close(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cur := range s.running {
		s.registry.Unregister(id)
		if err := cur.bm.Disconnect(ctx); err != nil {
			s.logger.Warn("integration broker disconnect error", "broker", id, "error", err)
		}
	}
	s.running = map[string]runningIntegrationBroker{}
}

// readStoredIntegrationBrokers returns every active integration.broker with its password, sorted by id.
func readStoredIntegrationBrokers(ctx context.Context, st integrationBrokerStore) ([]config.IntegrationBroker, error) {
	objs, err := st.ListConfigObjects(ctx, config.IntegrationBrokerConfigKind)
	if err != nil {
		return nil, err
	}
	out := make([]config.IntegrationBroker, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.IntegrationBrokerConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, err
		}
		p, err := config.DecodeIntegrationBrokerPayload(rev.PayloadJSON)
		if err != nil {
			return nil, err
		}
		pw, _, err := st.GetCredential(ctx, config.IntegrationBrokerConfigKind, obj.ID, config.IntegrationBrokerPasswordCredentialField)
		if err != nil {
			return nil, err
		}
		out = append(out, config.IntegrationBroker{ID: obj.ID, URL: p.URL, Username: p.Username, Password: pw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// builtInLoginFiles is the slice of *enrollment.BrokerFiles the built-in login needs.
type builtInLoginFiles interface {
	Check() error
	HasUser(username string) (bool, error)
	Provision(username string) (string, func() error, error)
}

// builtInLoginStore is where the integration login's password is kept.
type builtInLoginStore interface {
	GetCredential(ctx context.Context, kind, objectID, field string) (string, bool, error)
	SetCredential(ctx context.Context, kind, objectID, field, value string) error
}

// ensureBuiltInIntegrationBroker returns the built-in broker entry, creating
// its integration login first when the broker files lack it. It returns nil
// when this coordinator does not manage the built-in broker's logins.
func ensureBuiltInIntegrationBroker(ctx context.Context, cfg config.Config, files builtInLoginFiles, st builtInLoginStore, logger *slog.Logger) *config.IntegrationBroker {
	if cfg.BrokerMode != "builtin" {
		return nil
	}
	if err := files.Check(); err != nil {
		logger.Warn("the built-in broker is not offered for MQTT actions because the coordinator cannot manage its logins", "error", err)
		return nil
	}
	kind, id, field := config.IntegrationBrokerConfigKind, config.BuiltInIntegrationBrokerID, config.IntegrationBrokerPasswordCredentialField
	password, stored, err := st.GetCredential(ctx, kind, id, field)
	if err != nil {
		logger.Warn("the built-in broker is not offered for MQTT actions because its login could not be read", "error", err)
		return nil
	}
	hasUser, err := files.HasUser(config.BuiltInIntegrationBrokerUsername)
	if err != nil {
		logger.Warn("the built-in broker is not offered for MQTT actions because its password file could not be read", "error", err)
		return nil
	}
	if !stored || password == "" || !hasUser {
		var undo func() error
		password, undo, err = files.Provision(config.BuiltInIntegrationBrokerUsername)
		if err != nil {
			logger.Warn("the built-in broker is not offered for MQTT actions because its login could not be created", "error", err)
			return nil
		}
		if err := st.SetCredential(ctx, kind, id, field, password); err != nil {
			logger.Warn("the built-in broker is not offered for MQTT actions because its login could not be saved", "error", errors.Join(err, undo()))
			return nil
		}
		logger.Info("created the built-in broker's integration login for MQTT actions")
	}
	return &config.IntegrationBroker{
		ID: id, URL: cfg.MQTTBroker, Username: config.BuiltInIntegrationBrokerUsername,
		Password: password, TopicPrefix: config.BuiltInIntegrationTopicPrefix,
	}
}

var _ builtInLoginFiles = (*enrollment.BrokerFiles)(nil)

// migrateIntegrationBrokersFromEnv stores SHOWMESH_INTEGRATION_BROKERS once,
// when nothing is stored yet (ADR-039 decision 3). It returns the environment
// list as a fallback when the store cannot take it, and never refuses to start.
func migrateIntegrationBrokersFromEnv(ctx context.Context, st *store.Store, identitySvc identity.Service, env []config.IntegrationBroker, now func() time.Time, logger *slog.Logger) (fallback []config.IntegrationBroker) {
	if len(env) == 0 {
		return nil
	}
	objs, err := st.ListConfigObjects(ctx, config.IntegrationBrokerConfigKind)
	if err != nil {
		logger.Error("could not read the stored integration brokers; using SHOWMESH_INTEGRATION_BROKERS for this run", "error", err)
		return env
	}
	if len(objs) > 0 {
		logger.Warn("SHOWMESH_INTEGRATION_BROKERS is still set. The stored brokers are used and this variable is ignored; remove it and restart to allow edits.")
		return nil
	}
	err = identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		for _, b := range env {
			if b.ID == config.BuiltInIntegrationBrokerID {
				logger.Warn("SHOWMESH_INTEGRATION_BROKERS names built-in, which is now the built-in broker; that entry is not migrated")
				continue
			}
			payload, perr := config.EncodeIntegrationBrokerPayload(config.IntegrationBrokerPayload{URL: b.URL, Username: b.Username, PasswordSet: b.Password != ""})
			if perr != nil {
				return identity.AuditEntry{}, perr
			}
			if _, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
				Kind: config.IntegrationBrokerConfigKind, ObjectID: b.ID, Revision: 1, PayloadJSON: payload,
				Source: config.IntegrationBrokerSourceMigration,
				Note:   "migrated from SHOWMESH_INTEGRATION_BROKERS at coordinator startup",
			}); cerr != nil {
				return identity.AuditEntry{}, cerr
			}
			if _, aerr := tx.ActivateConfigRevision(ctx, config.IntegrationBrokerConfigKind, b.ID, 1); aerr != nil {
				return identity.AuditEntry{}, aerr
			}
			if b.Password != "" {
				if serr := tx.SetCredential(ctx, config.IntegrationBrokerConfigKind, b.ID, config.IntegrationBrokerPasswordCredentialField, b.Password); serr != nil {
					return identity.AuditEntry{}, serr
				}
			}
		}
		return identity.AuditEntry{
			Timestamp: now(), Action: "config.migrate", Target: config.IntegrationBrokerConfigKind,
			Params: map[string]any{"source": config.IntegrationBrokerSourceMigration, "brokerCount": len(env)},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		logger.Error("could not store SHOWMESH_INTEGRATION_BROKERS as integration brokers. The coordinator is using the "+
			"variable for this run and retries on the next start; do not remove it yet.", "error", err)
		return env
	}
	logger.Warn("stored SHOWMESH_INTEGRATION_BROKERS as integration brokers. Remove the variable and restart to allow edits.")
	return nil
}
