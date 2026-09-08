package fppconnectpush

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeConfigStore is an in-memory [ConfigStore] keyed by (kind, id),
// mirroring internal/coordinator/audioconfigpush's own test fake, extended
// with ListConfigObjects.
type fakeConfigStore struct {
	objects   map[string]store.ConfigObjectRecord
	revisions map[string]store.ConfigRevisionRecord
	byKind    map[string][]string
}

func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{
		objects:   map[string]store.ConfigObjectRecord{},
		revisions: map[string]store.ConfigRevisionRecord{},
		byKind:    map[string][]string{},
	}
}

func key(kind, id string) string { return kind + "/" + id }

func revisionKey(kind, id string, revision int64) string {
	return fmt.Sprintf("%s@%d", key(kind, id), revision)
}

func (f *fakeConfigStore) put(kind, id string, revision int64, payloadJSON string) {
	if _, ok := f.objects[key(kind, id)]; !ok {
		f.byKind[kind] = append(f.byKind[kind], id)
	}
	f.objects[key(kind, id)] = store.ConfigObjectRecord{Kind: kind, ID: id, CurrentRevision: revision}
	f.revisions[revisionKey(kind, id, revision)] = store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: revision, PayloadJSON: payloadJSON,
	}
}

func (f *fakeConfigStore) GetConfigObject(_ context.Context, kind, id string) (store.ConfigObjectRecord, error) {
	obj, ok := f.objects[key(kind, id)]
	if !ok {
		return store.ConfigObjectRecord{}, store.ErrConfigObjectNotFound
	}
	return obj, nil
}

func (f *fakeConfigStore) GetConfigRevision(_ context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error) {
	rev, ok := f.revisions[revisionKey(kind, id, revision)]
	if !ok {
		return store.ConfigRevisionRecord{}, store.ErrConfigObjectNotFound
	}
	return rev, nil
}

func (f *fakeConfigStore) ListConfigObjects(_ context.Context, kind string) ([]store.ConfigObjectRecord, error) {
	ids := f.byKind[kind]
	out := make([]store.ConfigObjectRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.objects[key(kind, id)])
	}
	return out, nil
}

func putSurface(f *fakeConfigStore, id, show, node string, start, count int) {
	payload := config.ShowSurfacePayload{
		Show: show, Name: id, Node: node,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: start, ChannelCount: count},
		Geometry:     config.ShowSurfaceGeometry{Width: 1, Height: count / 3, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: id}},
	}
	raw, err := config.EncodeShowSurfacePayload(payload)
	if err != nil {
		panic(err)
	}
	f.put(config.ShowSurfaceConfigKind, id, 1, raw)
}

func putShow(f *fakeConfigStore, id, name string) {
	raw, err := config.EncodeShowPayload(config.ShowPayload{Name: name})
	if err != nil {
		panic(err)
	}
	f.put(config.ShowConfigKind, id, 1, raw)
}

func putActiveShow(f *fakeConfigStore, showID string) {
	raw, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		panic(err)
	}
	f.put(config.ShowActiveConfigKind, config.ShowActiveObjectID, 1, raw)
}

func putSettings(f *fakeConfigStore, p config.FPPConnectSettingsPayload) {
	raw, err := config.EncodeFPPConnectSettingsPayload(p)
	if err != nil {
		panic(err)
	}
	f.put(config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID, 1, raw)
}

// putAssetSettings seeds "assets.settings" (FC3's own dependency, on top
// of the four kinds this package already watches): contentBaseUrl is the
// only field this package reads out of it.
func putAssetSettings(f *fakeConfigStore, s config.AssetSettings) {
	raw, err := config.EncodeAssetSettingsPayload(s)
	if err != nil {
		panic(err)
	}
	f.put(config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, 1, raw)
}

// fakePublisher records every publish, keyed by decoded action.
type fakePublisher struct {
	published []mqttproto.CmdPayload
	failNext  bool
}

func (p *fakePublisher) Publish(_ context.Context, _ string, _ byte, _ bool, payload []byte) error {
	if p.failNext {
		p.failNext = false
		return context.DeadlineExceeded
	}
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		return err
	}
	cmd, err := mqttproto.DecodeCmdPayload(env)
	if err != nil {
		return err
	}
	p.published = append(p.published, cmd)
	return nil
}

func (p *fakePublisher) last() (mqttproto.CmdPayload, bool) {
	if len(p.published) == 0 {
		return mqttproto.CmdPayload{}, false
	}
	return p.published[len(p.published)-1], true
}

func TestToNodeNoSurfacesPushesEmptyRanges(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, ok := pub.last()
	if !ok {
		t.Fatal("no fppconnect.configure command was published")
	}
	if cmd.Params["channelRanges"] != "" {
		t.Errorf("channelRanges = %v, want empty string", cmd.Params["channelRanges"])
	}
	if cmd.Params["activeShow"] != nil {
		t.Errorf("activeShow = %v, want nil", cmd.Params["activeShow"])
	}
	if cmd.Params["schema"] != schemaVersion {
		t.Errorf("schema = %v, want %v", cmd.Params["schema"], schemaVersion)
	}
}

func TestToNodeOneSurfaceStartingAtChannelOne(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	putSurface(cs, "surface-1", "front-yard", "node-1", 1, 150)
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["channelRanges"] != "0-149" {
		t.Errorf("channelRanges = %v, want 0-149", cmd.Params["channelRanges"])
	}
}

func TestToNodeTwoSurfacesOnOneNode(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	putSurface(cs, "surface-1", "front-yard", "node-1", 301, 150)
	putSurface(cs, "surface-2", "front-yard", "node-1", 1, 150)
	putSurface(cs, "surface-3", "front-yard", "node-2", 1, 150) // different node: excluded
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["channelRanges"] != "0-149,300-449" {
		t.Errorf("channelRanges = %v, want 0-149,300-449", cmd.Params["channelRanges"])
	}
}

// TestToNodeFormattingFailureSkipsRangeButStillPushes proves a surface
// whose combined range fails to format never withholds the rest of the
// push, and never produces "0-0".
func TestToNodeFormattingFailureSkipsRangeButStillPushes(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	// Twenty non-contiguous surfaces push the formatted string past the
	// 120-byte ping field.
	for i := 0; i < 20; i++ {
		putSurface(cs, fmt.Sprintf("surface-%d", i), "front-yard", "node-1", 1+i*200, 150)
	}
	putActiveShow(cs, "front-yard")
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", slog.Default(), nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["channelRanges"] != "" {
		t.Errorf("channelRanges = %v, want empty string on a formatting failure", cmd.Params["channelRanges"])
	}
	if cmd.Params["activeShow"] != "Front Yard" {
		t.Errorf("activeShow = %v, want Front Yard (unaffected by the range formatting failure)", cmd.Params["activeShow"])
	}
}

// TestToNodeFormattingFailureRecordsDroppedStatus proves a formatting
// failure that used to leave no trace outside the coordinator log (see
// TestToNodeFormattingFailureSkipsRangeButStillPushes above, which
// proves the SAME setup's published channelRanges is indistinguishable
// from TestToNodeNoSurfacesPushesEmptyRanges' legitimate empty case) is
// now readable back through the statusStore as "dropped", with a reason
// naming the actual refusal.
func TestToNodeFormattingFailureRecordsDroppedStatus(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	for i := 0; i < 20; i++ {
		putSurface(cs, fmt.Sprintf("surface-%d", i), "front-yard", "node-1", 1+i*200, 150)
	}
	pub := &fakePublisher{}
	statusStore := NewStatusStore()
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, statusStore); err != nil {
		t.Fatalf("ToNode: %v", err)
	}

	obs := statusStore.NodeFPPConnectObservations("node-1")
	if len(obs) != 2 {
		t.Fatalf("NodeFPPConnectObservations returned %d entries, want 2", len(obs))
	}
	state, reason := obs[0], obs[1]
	if state.Signal != SignalChannelRangeState || state.Value != ChannelRangeStateDropped {
		t.Errorf("state observation = %+v, want signal %q value %q", state, SignalChannelRangeState, ChannelRangeStateDropped)
	}
	reasonStr, ok := reason.Value.(string)
	if reason.Signal != SignalChannelRangeReason || !ok || reasonStr == "" {
		t.Errorf("reason observation = %+v, want a non-empty string reason", reason)
	}
	if !strings.Contains(reasonStr, "120-byte") {
		t.Errorf("reason = %q, want it to name the actual refusal (120-byte ping field)", reasonStr)
	}
}

// TestToNodeNoSurfacesRecordsNoSurfacesStatus proves a node with no
// configured surface at all records "no_surfaces", never "dropped" — the
// legitimate empty case must stay distinguishable from a real drop.
func TestToNodeNoSurfacesRecordsNoSurfacesStatus(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	statusStore := NewStatusStore()
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, statusStore); err != nil {
		t.Fatalf("ToNode: %v", err)
	}

	obs := statusStore.NodeFPPConnectObservations("node-1")
	if len(obs) != 2 {
		t.Fatalf("NodeFPPConnectObservations returned %d entries, want 2", len(obs))
	}
	if obs[0].Value != ChannelRangeStateNoSurfaces {
		t.Errorf("state = %v, want %q", obs[0].Value, ChannelRangeStateNoSurfaces)
	}
	if obs[1].Value != "" {
		t.Errorf("reason = %v, want empty string (not applicable)", obs[1].Value)
	}
}

// TestToNodeFormattedSurfaceRecordsFormattedStatus proves a node whose
// range formats successfully records "formatted".
func TestToNodeFormattedSurfaceRecordsFormattedStatus(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	putSurface(cs, "surface-1", "front-yard", "node-1", 1, 150)
	pub := &fakePublisher{}
	statusStore := NewStatusStore()
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, statusStore); err != nil {
		t.Fatalf("ToNode: %v", err)
	}

	obs := statusStore.NodeFPPConnectObservations("node-1")
	if len(obs) != 2 {
		t.Fatalf("NodeFPPConnectObservations returned %d entries, want 2", len(obs))
	}
	if obs[0].Value != ChannelRangeStateFormatted {
		t.Errorf("state = %v, want %q", obs[0].Value, ChannelRangeStateFormatted)
	}
	if obs[1].Value != "" {
		t.Errorf("reason = %v, want empty string (not applicable)", obs[1].Value)
	}
}

// TestToNodeNilStatusStoreIsANoOp proves a nil statusStore (an unwired
// dependency) never panics ToNode, matching every other nil-safe optional
// dependency in this package.
func TestToNodeNilStatusStoreIsANoOp(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	for i := 0; i < 20; i++ {
		putSurface(cs, fmt.Sprintf("surface-%d", i), "front-yard", "node-1", 1+i*200, 150)
	}
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
}

func TestToNodeActiveShowNullWhenNeverActivated(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["activeShow"] != nil {
		t.Errorf("activeShow = %v, want nil", cmd.Params["activeShow"])
	}
}

func TestToNodeActiveShowNameAndShowNamesList(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	putShow(cs, "back-yard", "Back Yard")
	putActiveShow(cs, "front-yard")
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["activeShow"] != "Front Yard" {
		t.Errorf("activeShow = %v, want Front Yard", cmd.Params["activeShow"])
	}
	names, ok := cmd.Params["showNames"].([]any)
	if !ok || len(names) != 2 {
		t.Fatalf("showNames = %v, want a 2-element list", cmd.Params["showNames"])
	}
}

// TestToNodePushesShowsIDNameList proves the additive "shows" field (FC3,
// ADR-028 decision 8) carries every show's config object id paired with
// its display name, sorted by id, alongside the existing name-only
// "showNames" field xLights' own surface still reads.
func TestToNodePushesShowsIDNameList(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	putShow(cs, "back-yard", "Back Yard")
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	shows, ok := cmd.Params["shows"].([]any)
	if !ok || len(shows) != 2 {
		t.Fatalf("shows = %v, want a 2-element list", cmd.Params["shows"])
	}
	// Sorted by id: "back-yard" before "front-yard".
	first, ok := shows[0].(map[string]any)
	if !ok || first["id"] != "back-yard" || first["name"] != "Back Yard" {
		t.Errorf("shows[0] = %v, want {id: back-yard, name: Back Yard}", shows[0])
	}
	second, ok := shows[1].(map[string]any)
	if !ok || second["id"] != "front-yard" || second["name"] != "Front Yard" {
		t.Errorf("shows[1] = %v, want {id: front-yard, name: Front Yard}", shows[1])
	}
}

// TestToNodePushesEmptyShowsListWhenNoShowsExist proves an empty shows
// list, not an omitted key or null, matches showNames' own no-shows shape.
func TestToNodePushesEmptyShowsListWhenNoShowsExist(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	shows, ok := cmd.Params["shows"].([]any)
	if !ok || len(shows) != 0 {
		t.Fatalf("shows = %v, want an empty list", cmd.Params["shows"])
	}
}

// TestIdempotencyKeyChangesWhenAShowIsRenamed proves a display-name-only
// change (the show's id is untouched) still changes the idempotency key,
// since the "shows" content is hashed independently of showNames.
func TestIdempotencyKeyChangesWhenAShowIsRenamed(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")

	resolved1, _, err := resolveForNode(context.Background(), cs, "node-1", nil)
	if err != nil {
		t.Fatalf("resolveForNode: %v", err)
	}
	key1 := idempotencyKeyFor("node-1", resolved1)

	putShow(cs, "front-yard", "Front Yard Renamed")
	resolved2, _, err := resolveForNode(context.Background(), cs, "node-1", nil)
	if err != nil {
		t.Fatalf("resolveForNode: %v", err)
	}
	key2 := idempotencyKeyFor("node-1", resolved2)

	if key1 == key2 {
		t.Fatalf("idempotency key did not change when a show was renamed: both %q", key1)
	}
}

func TestToNodePushesDefaultSettingsWhenUnconfigured(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	settings, ok := cmd.Params["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings = %v, want a map", cmd.Params["settings"])
	}
	if settings["enabled"] != config.FPPConnectSettingsDefaultPayload.Enabled {
		t.Errorf("settings.enabled = %v, want %v", settings["enabled"], config.FPPConnectSettingsDefaultPayload.Enabled)
	}
	if int64(settings["maxFileBytes"].(float64)) != config.FPPConnectSettingsDefaultPayload.MaxFileBytes {
		t.Errorf("settings.maxFileBytes = %v, want %v", settings["maxFileBytes"], config.FPPConnectSettingsDefaultPayload.MaxFileBytes)
	}
}

func TestToNodePushesConfiguredSettings(t *testing.T) {
	cs := newFakeConfigStore()
	putSettings(cs, config.FPPConnectSettingsPayload{Enabled: false, MaxFileBytes: 100, MaxAssetDirBytes: 1000})
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	settings := cmd.Params["settings"].(map[string]any)
	if settings["enabled"] != false {
		t.Errorf("settings.enabled = %v, want false", settings["enabled"])
	}
}

// TestToNodePushesEmptyCoordinatorBaseURLWhenUnconfigured proves a
// coordinator whose assets.settings has never been written pushes ""
// for coordinatorBaseUrl, not an omitted key: FC3's registrar on the
// agent side reads absence and "" identically, but this coordinator-side
// resolution must still match assetsync.Service.ContentBaseURL's own
// "empty is a real, deliberate state" default (config.DefaultAssetSettings).
func TestToNodePushesEmptyCoordinatorBaseURLWhenUnconfigured(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["coordinatorBaseUrl"] != "" {
		t.Errorf("coordinatorBaseUrl = %v, want empty string when assets.settings has never been written", cmd.Params["coordinatorBaseUrl"])
	}
}

// TestToNodePushesConfiguredCoordinatorBaseURL proves an operator-set
// assets.settings.contentBaseUrl reaches the push verbatim, resolved the
// same way assetsync.Service.fetchURL resolves it.
func TestToNodePushesConfiguredCoordinatorBaseURL(t *testing.T) {
	cs := newFakeConfigStore()
	putAssetSettings(cs, config.AssetSettings{ContentBaseURL: "http://coordinator.example:8080"})
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["coordinatorBaseUrl"] != "http://coordinator.example:8080" {
		t.Errorf("coordinatorBaseUrl = %v, want http://coordinator.example:8080", cmd.Params["coordinatorBaseUrl"])
	}
}

// TestIdempotencyKeyChangesWhenCoordinatorBaseURLChanges proves a
// contentBaseUrl change alone (no other of the four watched kinds
// touched) still changes the idempotency key, since currentCoordinatorBaseURL
// contributes its own revision tuple to the fingerprint.
func TestIdempotencyKeyChangesWhenCoordinatorBaseURLChanges(t *testing.T) {
	cs := newFakeConfigStore()
	putAssetSettings(cs, config.AssetSettings{ContentBaseURL: "http://coordinator.example:8080"})

	resolved1, _, err := resolveForNode(context.Background(), cs, "node-1", nil)
	if err != nil {
		t.Fatalf("resolveForNode: %v", err)
	}
	key1 := idempotencyKeyFor("node-1", resolved1)

	putAssetSettings(cs, config.AssetSettings{ContentBaseURL: "http://coordinator.example:9090"})
	resolved2, _, err := resolveForNode(context.Background(), cs, "node-1", nil)
	if err != nil {
		t.Fatalf("resolveForNode: %v", err)
	}
	key2 := idempotencyKeyFor("node-1", resolved2)

	if key1 == key2 {
		t.Fatalf("idempotency key did not change when coordinatorBaseUrl changed: both %q", key1)
	}
}

// TestIdempotencyKeyStableWhenNothingChangedButChangesWhenSomethingDoes
// proves the same resolved state produces the same key across two calls,
// and a changed input (a new surface) produces a different one.
func TestIdempotencyKeyStableWhenNothingChangedButChangesWhenSomethingDoes(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "front-yard", "Front Yard")
	putSurface(cs, "surface-1", "front-yard", "node-1", 1, 150)

	pub1 := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub1, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (1): %v", err)
	}
	cmd1, _ := pub1.last()

	pub2 := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub2, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (2): %v", err)
	}
	cmd2, _ := pub2.last()

	if cmd1.IdempotencyKey != cmd2.IdempotencyKey {
		t.Errorf("idempotency key changed with no input change: %q vs %q", cmd1.IdempotencyKey, cmd2.IdempotencyKey)
	}

	putSurface(cs, "surface-2", "front-yard", "node-1", 301, 150)
	pub3 := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub3, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (3): %v", err)
	}
	cmd3, _ := pub3.last()
	if cmd3.IdempotencyKey == cmd1.IdempotencyKey {
		t.Error("idempotency key did not change after a new surface was added")
	}
}

// TestIdempotencyKeyNeverRepeatsAcrossARevertToAnEarlierValue proves the
// regression this seam's review caught: show.active set to A, then B, then
// back to A must produce three DISTINCT idempotency keys, even though the
// resolved activeShow content on the first and third push is identical.
// A key that repeated here would collide with the agent's own
// capacity-bounded idempotency cache (internal/agent/command.go), which
// would then silently refuse to re-apply the third push as an exact
// replay of the first, the node would keep advertising B's active show
// forever.
func TestIdempotencyKeyNeverRepeatsAcrossARevertToAnEarlierValue(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "halloween", "Halloween")
	putShow(cs, "christmas", "Christmas")

	rawHalloween, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: "halloween"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rawChristmas, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: "christmas"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Revision 1: Halloween.
	cs.put(config.ShowActiveConfigKind, config.ShowActiveObjectID, 1, rawHalloween)
	pub1 := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub1, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (1): %v", err)
	}
	cmd1, _ := pub1.last()
	if cmd1.Params["activeShow"] != "Halloween" {
		t.Fatalf("push 1 activeShow = %v, want Halloween", cmd1.Params["activeShow"])
	}

	// Revision 2: Christmas.
	cs.put(config.ShowActiveConfigKind, config.ShowActiveObjectID, 2, rawChristmas)
	pub2 := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub2, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (2): %v", err)
	}
	cmd2, _ := pub2.last()

	// Revision 3: back to Halloween, identical resolved content to
	// revision 1's push, but a genuinely new, later revision.
	cs.put(config.ShowActiveConfigKind, config.ShowActiveObjectID, 3, rawHalloween)
	pub3 := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub3, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (3): %v", err)
	}
	cmd3, _ := pub3.last()
	if cmd3.Params["activeShow"] != "Halloween" {
		t.Fatalf("push 3 activeShow = %v, want Halloween", cmd3.Params["activeShow"])
	}

	if cmd1.IdempotencyKey == cmd2.IdempotencyKey {
		t.Error("push 1 and push 2 share an idempotency key")
	}
	if cmd2.IdempotencyKey == cmd3.IdempotencyKey {
		t.Error("push 2 and push 3 share an idempotency key")
	}
	if cmd1.IdempotencyKey == cmd3.IdempotencyKey {
		t.Error("push 1 (Halloween) and push 3 (Halloween again, a later revision) share an idempotency key, " +
			"a revert to an earlier value must still mint a fresh key")
	}
}

// TestIdempotencyKeyNeverRepeatsWhenASurfaceIsMovedOffANode proves the
// blocking regression this seam's second review round caught: node-1's
// revision fingerprint used to be filtered to "surfaces currently on
// node-1", so a surface added to node-1 and then moved to node-2 left
// node-1 with the SAME (empty) contributing-revision set it had before
// the surface ever existed, and the SAME (empty) resolved content, a key
// collision with node-1's very first, surface-less push. The agent's
// capacity-bounded idempotency cache (internal/agent/command.go) would
// then treat the vacating push as a replay of that first push and never
// re-apply it, leaving node-1 advertising a range it no longer owns.
func TestIdempotencyKeyNeverRepeatsWhenASurfaceIsMovedOffANode(t *testing.T) {
	cs := newFakeConfigStore()
	putShow(cs, "halloween", "Halloween")

	// Baseline: node-1 has never had a surface.
	baselinePub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, baselinePub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (baseline): %v", err)
	}
	baselineCmd, _ := baselinePub.last()
	if baselineCmd.Params["channelRanges"] != "" {
		t.Fatalf("baseline channelRanges = %v, want empty", baselineCmd.Params["channelRanges"])
	}

	// Revision 1: the surface is created on node-1.
	putSurface(cs, "garage-door", "halloween", "node-1", 1, 150)
	addedPub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, addedPub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (added): %v", err)
	}
	addedCmd, _ := addedPub.last()
	if addedCmd.Params["channelRanges"] != "0-149" {
		t.Fatalf("added channelRanges = %v, want 0-149", addedCmd.Params["channelRanges"])
	}

	// Revision 2: the surface moves to node-2, node-1's push (the
	// vacated node) is the one under test.
	movedSurface := config.ShowSurfacePayload{
		Show: "halloween", Name: "garage-door", Node: "node-2",
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 150},
		Geometry:     config.ShowSurfaceGeometry{Width: 1, Height: 50, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "garage-door"}},
	}
	movedRaw, err := config.EncodeShowSurfacePayload(movedSurface)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cs.put(config.ShowSurfaceConfigKind, "garage-door", 2, movedRaw)
	vacatedPub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, vacatedPub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (vacated): %v", err)
	}
	vacatedCmd, _ := vacatedPub.last()
	if vacatedCmd.Params["channelRanges"] != "" {
		t.Fatalf("vacated channelRanges = %v, want empty", vacatedCmd.Params["channelRanges"])
	}

	if baselineCmd.IdempotencyKey == vacatedCmd.IdempotencyKey {
		t.Error("baseline (never had a surface) and vacated (surface added then moved away) push share an " +
			"idempotency key, the agent's idempotency cache would treat the vacating push as an already-seen " +
			"replay and never re-apply it")
	}
	if addedCmd.IdempotencyKey == vacatedCmd.IdempotencyKey {
		t.Error("added and vacated pushes share an idempotency key")
	}

	// Revision 3: the surface moves back onto node-1, the revert case.
	cs.put(config.ShowSurfaceConfigKind, "garage-door", 3, addedSurfaceRaw(t))
	revertPub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, revertPub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode (revert): %v", err)
	}
	revertCmd, _ := revertPub.last()
	if revertCmd.Params["channelRanges"] != "0-149" {
		t.Fatalf("revert channelRanges = %v, want 0-149", revertCmd.Params["channelRanges"])
	}
	if revertCmd.IdempotencyKey == addedCmd.IdempotencyKey {
		t.Error("added and revert (a later revision, back on node-1) pushes share an idempotency key, " +
			"a revert to an earlier value must still mint a fresh key")
	}
	if revertCmd.IdempotencyKey == baselineCmd.IdempotencyKey || revertCmd.IdempotencyKey == vacatedCmd.IdempotencyKey {
		t.Error("revert push collides with an earlier no-surface push")
	}
}

// addedSurfaceRaw returns garage-door's original (node-1) payload, JSON
// encoded, for TestIdempotencyKeyNeverRepeatsWhenASurfaceIsMovedOffANode's
// revert step.
func addedSurfaceRaw(t *testing.T) string {
	t.Helper()
	payload := config.ShowSurfacePayload{
		Show: "halloween", Name: "garage-door", Node: "node-1",
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 150},
		Geometry:     config.ShowSurfaceGeometry{Width: 1, Height: 50, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "garage-door"}},
	}
	raw, err := config.EncodeShowSurfacePayload(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}

// TestActiveShowUnresolvableReferenceReportsNilNotRawID proves that when
// show.active names a show id with no active "show" revision (a stale
// pointer left behind by a store inconsistency elsewhere), the pushed
// activeShow is nil, never the raw, unresolvable id, a value that would
// appear in no showNames entry and could never be selected in FPP
// Connect's playlist dropdown (ADR-044 decision 8: a wrong silent guess is
// worse than an honest "cannot resolve").
func TestActiveShowUnresolvableReferenceReportsNilNotRawID(t *testing.T) {
	cs := newFakeConfigStore()
	// show.active points at "ghost-show", which has never been written as
	// a "show" object at all.
	putActiveShow(cs, "ghost-show")

	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	cmd, _ := pub.last()
	if cmd.Params["activeShow"] != nil {
		t.Errorf("activeShow = %v, want nil for an unresolvable show.active reference", cmd.Params["activeShow"])
	}
}

// TestBestEffortNeverPanicsOnPublishFailure proves BestEffort swallows a
// publish failure rather than propagating or panicking, the write or
// hello that triggered it must never fail because of this push.
func TestBestEffortNeverPanicsOnPublishFailure(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{failNext: true}
	BestEffort(context.Background(), cs, pub, time.Now, "any-node", nil, nil)
}

// TestEachWriteProducesExactlyOnePushPerAffectedNode proves ToNode itself
// publishes exactly one "fppconnect.configure" command per call, a
// caller pushing to N affected nodes (a coordinator write hook) gets
// exactly N publishes, one per ToNode call, never more.
func TestEachWriteProducesExactlyOnePushPerAffectedNode(t *testing.T) {
	cs := newFakeConfigStore()
	pub := &fakePublisher{}
	if err := ToNode(context.Background(), cs, pub, time.Now, "node-1", nil, nil); err != nil {
		t.Fatalf("ToNode: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published %d commands, want exactly 1", len(pub.published))
	}
}
