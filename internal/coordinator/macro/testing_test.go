package macro

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is this package's own test scaffolding, mirroring
// internal/coordinator/api's own standing rule (auth_test.go's top doc
// comment): a REAL *store.Store and a REAL identity.Service over a
// throwaway on-disk SQLite database, never a hand-rolled fake for either —
// this package cannot import another package's _test.go helpers, so the
// pattern is duplicated here rather than shared.

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestStoreAndIdentity opens a real store.Store and a real
// identity.Service sharing nowFn's notion of "now", both cleaned up via
// t.Cleanup. storeDir is returned so a test can open a second raw
// connection to install a fault-injecting SQLite trigger (installFailAuditTrigger
// below), mirroring internal/coordinator/api/config_test.go's identical
// pattern for the identical reason: a real trigger is the only thing that
// proves ADR-024 decision 11's fail-closed rule against something other
// than a mock.
func newTestStoreAndIdentity(t *testing.T, nowFn func() time.Time) (*store.Store, identity.Service, string) {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(nowFn))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, nowFn, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return st, svc, storeDir
}

// installFailAuditTrigger makes every INSERT into audit_log fail from this
// point forward, on the real SQLite database backing st — the same
// mechanism internal/coordinator/api/config_test.go and
// internal/coordinator/identity/audited_write_test.go already use to prove
// ADR-024 decision 11's fail-closed rule against real SQLite rather than a
// mocked error.
func installFailAuditTrigger(t *testing.T, storeDir string) {
	t.Helper()
	dbPath := filepath.Join(storeDir, "showmesh.db")
	// _busy_timeout matches store.Open's own connection (store.go), which
	// this raw second connection does not otherwise get: this package's
	// own tests call this helper WHILE a background run goroutine may
	// still be mid-write on the store's own connection (unlike
	// internal/coordinator/api/config_test.go's use of the identical
	// pattern, which always calls it before any concurrent write is
	// possible), so without a busy timeout this CREATE TRIGGER DDL can
	// race a live write and fail SQLITE_BUSY — observed directly: about
	// 1 run in 5 without this parameter, across five repeated runs of
	// this package's audit_exemption_test.go tests together.
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open raw connection to %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	_, err = raw.ExecContext(context.Background(), `
		CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END;
	`)
	if err != nil {
		t.Fatalf("install fail_audit trigger: %v", err)
	}
}

// putAction writes a show.action config object at revision 1, current,
// mirroring what a real PUT /api/v1/config/show.action/{id} would leave
// behind (this package does not exercise that write path itself — it is
// Wave 2's other builders' work — so this helper builds the row shape
// directly against the store, the same way this package's own resolve.go
// reads it back).
func putAction(t *testing.T, st *store.Store, id string, payload config.ShowActionPayload) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, config.ShowActionConfigKind, id); err != nil {
		t.Fatalf("create action object %q: %v", id, err)
	}
	payloadJSON, err := config.EncodeShowActionPayload(payload)
	if err != nil {
		t.Fatalf("encode action payload %q: %v", id, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActionConfigKind, ObjectID: id, Revision: 1, PayloadJSON: payloadJSON,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create action revision %q: %v", id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActionConfigKind, id, 1); err != nil {
		t.Fatalf("activate action revision %q: %v", id, err)
	}
}

// putMacro is putAction's show.macro equivalent.
func putMacro(t *testing.T, st *store.Store, id string, payload config.ShowMacroPayload) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, config.ShowMacroConfigKind, id); err != nil {
		t.Fatalf("create macro object %q: %v", id, err)
	}
	payloadJSON, err := config.EncodeShowMacroPayload(payload)
	if err != nil {
		t.Fatalf("encode macro payload %q: %v", id, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowMacroConfigKind, ObjectID: id, Revision: 1, PayloadJSON: payloadJSON,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create macro revision %q: %v", id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowMacroConfigKind, id, 1); err != nil {
		t.Fatalf("activate macro revision %q: %v", id, err)
	}
}

// fppAction/mqttAction/testStep/testMacroPayload build the smallest valid
// payloads this package's own decode expects, for a test that does not
// care about the fields it leaves at their zero value.
func fppAction(instanceID, primitive, safetyClass string, params map[string]any) config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "test-show", Label: "test action", SafetyClass: safetyClass,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationFPP,
			InstanceID:  instanceID, Primitive: primitive, Params: params,
		},
	}
}

func mqttAction(brokerID, safetyClass string, expect config.ShowActionMQTTExpect) config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "test-show", Label: "test mqtt action", SafetyClass: safetyClass,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationMQTT,
			Broker:      brokerID,
			Publish:     &config.ShowActionMQTTPublish{Topic: "test/publish", Payload: "on", QoS: 1, Retain: false},
			Expect:      &expect,
		},
	}
}

func testStep(id, action string) config.ShowMacroStep {
	return config.ShowMacroStep{
		ID: id, Action: action,
		OnFailure: config.ShowMacroOnFailureDefault, OnUnconfirmed: config.ShowMacroOnUnconfirmedDefault,
		LocalFallback: config.ShowMacroLocalFallback{Class: config.ShowMacroLocalFallbackNone, Reason: "test fixture"},
	}
}

func testMacroPayload(steps ...config.ShowMacroStep) config.ShowMacroPayload {
	return config.ShowMacroPayload{Show: "test-show", Label: "test macro", Steps: steps}
}

// --- fppDispatcher fake. ---

type fakeDispatcher struct {
	mu    sync.Mutex
	calls []api.FPPCommandInput

	dispatchFn func(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error)
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, in api.FPPCommandInput) (api.FPPCommandOutcome, *v1.Problem, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	if f.dispatchFn != nil {
		return f.dispatchFn(ctx, in)
	}
	now := time.Now()
	return api.FPPCommandOutcome{
		CommandID: "cmd-" + in.IdempotencyKey, Action: in.Action, InstanceID: in.InstanceID, Params: in.Params,
		Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "test evidence confirmed",
		DispatchedAt: ptrTime(now), ResolvedAt: ptrTime(now),
	}, nil, nil
}

func (f *fakeDispatcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// lastInput returns the most recent [api.FPPCommandInput] this fake was
// handed, so a test can assert on what actually reached the dispatch seam
// rather than on what an intermediate layer returned.
func (f *fakeDispatcher) lastInput() api.FPPCommandInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return api.FPPCommandInput{}
	}
	return f.calls[len(f.calls)-1]
}

// --- mqttRegistry fake. ---

type fakeBrokers struct {
	publishFn func(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error
	awaitFn   func(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error)
}

func (f *fakeBrokers) Publish(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error {
	if f.publishFn != nil {
		return f.publishFn(ctx, id, topic, qos, retain, payload)
	}
	return nil
}

func (f *fakeBrokers) AwaitResponse(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error) {
	if f.awaitFn != nil {
		return f.awaitFn(ctx, id, req)
	}
	return broker.Message{Topic: req.ResponseTopic, Payload: []byte("true")}, nil
}

// newTestExecutor builds an Executor with a real store/identity and
// caller-supplied fakes for the dispatch/broker seams. The returned func
// reports how many times Notify has fired so far.
func newTestExecutor(t *testing.T, st *store.Store, svc identity.Service, dispatch fppDispatcher, brokers mqttRegistry) (*Executor, func() int) {
	t.Helper()
	var mu sync.Mutex
	notifyCount := 0
	notify := func() {
		mu.Lock()
		notifyCount++
		mu.Unlock()
	}
	countFn := func() int {
		mu.Lock()
		defer mu.Unlock()
		return notifyCount
	}
	e := NewExecutor(Dependencies{
		Store: st, Identity: svc, Dispatch: dispatch, Brokers: brokers,
		Notify: notify, Clock: time.Now, Logger: testLogger(),
	}, Options{})
	return e, countFn
}

func testIssuer() api.FPPCommandIssuer {
	return api.FPPCommandIssuer{PrincipalID: "p1", PrincipalName: "tester", Form: identity.FormToken, CredentialID: "cred-1"}
}
