package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

type fakeLoginFiles struct {
	checkErr    error
	users       map[string]bool
	provisioned int
	undone      int
}

func (f *fakeLoginFiles) Check() error { return f.checkErr }
func (f *fakeLoginFiles) HasUser(u string) (bool, error) {
	return f.users[u], nil
}
func (f *fakeLoginFiles) Provision(u string) (string, func() error, error) {
	f.provisioned++
	f.users[u] = true
	return "fresh-password", func() error { f.undone++; return nil }, nil
}

type memLoginStore struct {
	values map[string]string
	setErr error
}

func (m *memLoginStore) GetCredential(_ context.Context, kind, id, field string) (string, bool, error) {
	v, ok := m.values[kind+"/"+id+"/"+field]
	return v, ok, nil
}
func (m *memLoginStore) SetCredential(_ context.Context, kind, id, field, value string) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.values[kind+"/"+id+"/"+field] = value
	return nil
}

var builtInCfg = config.Config{BrokerMode: "builtin", MQTTBroker: "tcp://mosquitto:1883"}

func TestBuiltInIntegrationBrokerCreatesItsLoginOnce(t *testing.T) {
	files := &fakeLoginFiles{users: map[string]bool{}}
	st := &memLoginStore{values: map[string]string{}}
	ctx := context.Background()

	b := ensureBuiltInIntegrationBroker(ctx, builtInCfg, files, st, discardLogger())
	if b == nil || files.provisioned != 1 {
		t.Fatalf("broker = %+v, provisioned %d times, want one new login", b, files.provisioned)
	}
	want := config.IntegrationBroker{ID: "built-in", URL: "tcp://mosquitto:1883", Username: "integration", Password: "fresh-password", TopicPrefix: "showmesh-actions/"}
	if *b != want {
		t.Fatalf("broker = %+v, want %+v", *b, want)
	}

	if again := ensureBuiltInIntegrationBroker(ctx, builtInCfg, files, st, discardLogger()); again == nil || files.provisioned != 1 || again.Password != "fresh-password" {
		t.Fatalf("second start provisioned %d times and returned %+v, want the stored login reused", files.provisioned, again)
	}

	delete(files.users, "integration")
	if ensureBuiltInIntegrationBroker(ctx, builtInCfg, files, st, discardLogger()); files.provisioned != 2 {
		t.Fatalf("a login missing from the password file was not recreated: provisioned %d times", files.provisioned)
	}
}

func TestBuiltInIntegrationBrokerIsNotOfferedWhenItCannotBeManaged(t *testing.T) {
	ctx := context.Background()
	external := builtInCfg
	external.BrokerMode = "external"
	if b := ensureBuiltInIntegrationBroker(ctx, external, &fakeLoginFiles{users: map[string]bool{}}, &memLoginStore{values: map[string]string{}}, discardLogger()); b != nil {
		t.Fatalf("external broker mode offered %+v", b)
	}
	unmanaged := &fakeLoginFiles{users: map[string]bool{}, checkErr: errors.New("not mounted")}
	if b := ensureBuiltInIntegrationBroker(ctx, builtInCfg, unmanaged, &memLoginStore{values: map[string]string{}}, discardLogger()); b != nil || unmanaged.provisioned != 0 {
		t.Fatalf("unmanaged broker files offered %+v and provisioned %d times", b, unmanaged.provisioned)
	}
	files := &fakeLoginFiles{users: map[string]bool{}}
	if b := ensureBuiltInIntegrationBroker(ctx, builtInCfg, files, &memLoginStore{values: map[string]string{}, setErr: errors.New("disk full")}, discardLogger()); b != nil || files.undone != 1 {
		t.Fatalf("a login that could not be saved offered %+v and was undone %d times, want nil and undone once", b, files.undone)
	}
}

func putStoredBroker(t *testing.T, st *store.Store, id, url string, revision int64) {
	t.Helper()
	ctx := context.Background()
	payload, err := config.EncodeIntegrationBrokerPayload(config.IntegrationBrokerPayload{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{Kind: config.IntegrationBrokerConfigKind, ObjectID: id, Revision: revision, PayloadJSON: payload, Source: "api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.IntegrationBrokerConfigKind, id, revision); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationBrokerSetFollowsTheStoredBrokers(t *testing.T) {
	st, _ := newTestAssetSettingsSyncDeps(t)
	ctx := context.Background()
	builtIn := config.IntegrationBroker{ID: "built-in", URL: "tcp://127.0.0.1:1", Username: "integration", TopicPrefix: "showmesh-actions/"}
	s := newIntegrationBrokerSet(st, "test", &builtIn, nil, discardLogger())
	var connected []string
	s.connect = func(ctx context.Context, b config.IntegrationBroker) (*broker.BrokerManager, error) {
		connected = append(connected, b.ID+"="+b.URL)
		return s.connectBroker(ctx, b)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.Close(ctx)
	})

	s.reconcile(ctx)
	if got := s.registry.IDs(); len(got) != 1 || got[0] != "built-in" {
		t.Fatalf("registered %v, want only built-in", got)
	}

	putStoredBroker(t, st, "ha", "tcp://127.0.0.1:2", 1)
	s.reconcile(ctx)
	if _, err := s.registry.Get("ha"); err != nil {
		t.Fatalf("added broker not registered: %v", err)
	}

	putStoredBroker(t, st, "ha", "tcp://127.0.0.1:3", 2)
	s.reconcile(ctx)
	if last := connected[len(connected)-1]; last != "ha=tcp://127.0.0.1:3" {
		t.Fatalf("changed broker was not reconnected: %v", connected)
	}

	if _, err := st.TombstoneConfigObject(ctx, config.IntegrationBrokerConfigKind, "ha"); err != nil {
		t.Fatal(err)
	}
	s.reconcile(ctx)
	if _, err := s.registry.Get("ha"); !errors.Is(err, broker.ErrUnknownBroker) {
		t.Fatalf("removed broker still resolves: %v", err)
	}
	if len(connected) != 3 {
		t.Fatalf("connections = %v, want built-in, ha, and ha again after its change", connected)
	}
}

func TestIntegrationBrokersMigrateFromTheVariableOnce(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)
	ctx := context.Background()
	env := []config.IntegrationBroker{{ID: "home-automation", URL: "tcp://10.0.0.5:1883", Username: "showmesh", Password: "pw"}}

	if fallback := migrateIntegrationBrokersFromEnv(ctx, st, svc, env, time.Now, discardLogger()); fallback != nil {
		t.Fatalf("fallback = %+v, want nil after a stored migration", fallback)
	}
	stored, err := readStoredIntegrationBrokers(ctx, st)
	if err != nil || len(stored) != 1 || stored[0] != env[0] {
		t.Fatalf("stored = %+v, %v, want the variable's broker with its password", stored, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.IntegrationBrokerConfigKind, "home-automation", 1)
	if err != nil || rev.Source != config.IntegrationBrokerSourceMigration {
		t.Fatalf("revision = %+v, %v", rev, err)
	}

	changed := []config.IntegrationBroker{{ID: "other", URL: "tcp://10.0.0.9:1883"}}
	if fallback := migrateIntegrationBrokersFromEnv(ctx, st, svc, changed, time.Now, discardLogger()); fallback != nil {
		t.Fatalf("second start fallback = %+v", fallback)
	}
	if stored, _ := readStoredIntegrationBrokers(ctx, st); len(stored) != 1 || stored[0].ID != "home-automation" {
		t.Fatalf("a later start changed the stored brokers: %+v", stored)
	}
}

func TestIntegrationBrokerMigrationFallsBackWhenItCannotBeStored(t *testing.T) {
	st, svc, dir := newTestAssetSettingsSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "audit_log")
	env := []config.IntegrationBroker{{ID: "home-automation", URL: "tcp://10.0.0.5:1883"}}
	fallback := migrateIntegrationBrokersFromEnv(context.Background(), st, svc, env, time.Now, discardLogger())
	if len(fallback) != 1 || fallback[0].ID != "home-automation" {
		t.Fatalf("fallback = %+v, want the variable's brokers", fallback)
	}
}
