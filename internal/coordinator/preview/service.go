package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ReconcileInterval = 10 * time.Second

	GatewayNotConfigured = "not_configured"
	GatewayConnecting    = "connecting"
	GatewayReachable     = "reachable"
	GatewayUnreachable   = "unreachable"

	KindNDI    = "ndi"
	KindCamera = "camera"

	TileLive       = "live"
	TileIdle       = "idle"
	TileConnecting = "connecting"
	TileNotFound   = "not_found"
	TileDisabled   = "disabled"
	TileError      = "error"

	KeyPrefixNDI    = "ndi:"
	KeyPrefixCamera = "camera:"
)

// Messages an operator sees. Each states the fact, then the action.
const (
	msgNotConfigured = "No preview gateway is set up. Add its address in Settings to see previews."
	msgConnecting    = "The preview gateway has not answered yet. Wait a few seconds and reload."
	msgUnreachable   = "The preview gateway cannot be reached. Check that it is running and that its address and token are right."
	msgSlowStart     = "The preview took too long to start. Try again in a few seconds."
	msgNDIGone       = "This source is no longer announced on the network. Check that its sender is running."
	msgCameraGone    = "The camera did not answer. Check its address and that it is on."
	msgDisabled      = "This camera is turned off. Turn it on to see it here."
	msgNDIDisabled   = "This NDI source is turned off. Turn it on to see it here."
	msgTileError     = "The preview could not start. Check the gateway log."
	msgCameraRefused = "The gateway did not accept the camera addresses. Check each address and save again."
)

var (
	// ErrUnknownKey means no tile has the requested key.
	ErrUnknownKey = errors.New("preview: unknown tile key")
	// ErrNotConfigured means no gateway address is set.
	ErrNotConfigured = errors.New("preview: gateway not configured")
)

// UnavailableError is a gateway failure with a message safe to show an operator.
type UnavailableError struct{ Message string }

func (e *UnavailableError) Error() string { return e.Message }

// Camera is one stored preview.camera object with its stream URL.
type Camera struct {
	ID      string
	Name    string
	Enabled bool
	URL     string
}

// NDISource is one stored preview.ndi object: an NDI source added by hand.
type NDISource struct {
	ID      string
	Label   string
	Name    string
	Address string
	Enabled bool
}

// Surface is one show.surface that renders to NDI.
type Surface struct {
	NodeID     string
	SurfaceID  string
	SourceName string
}

// Node names the ways an NDI host name can refer to a node.
type Node struct {
	ID    string
	Names []string
}

// Layout is the stored tile order and hidden tiles.
type Layout struct {
	Order  []string
	Hidden []string
}

// Settings is everything the service reads from the store on each pass.
type Settings struct {
	GatewayURL string
	Token      string
	Cameras    []Camera
	NDISources []NDISource
	Layout     Layout
	Surfaces   []Surface
	Nodes      []Node
}

// SettingsSource loads the current settings.
type SettingsSource interface {
	Load(ctx context.Context) (Settings, error)
}

// GatewayStatus is the coordinator's latest view of the gateway.
type GatewayStatus struct {
	State      string
	Message    string
	ObservedAt time.Time
}

// Tile is one dashboard preview tile.
type Tile struct {
	Key        string
	Kind       string
	Label      string
	NodeID     string
	SurfaceID  string
	CameraID   string
	State      string
	Message    string
	Hidden     bool
	ObservedAt time.Time
}

// Overview is the gateway state and every tile in layout order.
type Overview struct {
	Gateway GatewayStatus
	Tiles   []Tile
}

type observation struct {
	// gateway identifies the address and token the observation was made for.
	gateway string
	status  GatewayStatus
	sources []Source
	streams map[string]StreamState
	// camerasRefused is set when the gateway answered the stream set with a
	// client error and only the NDI streams were applied.
	camerasRefused bool
}

// Service reconciles the gateway's stream set and answers the dashboard.
type Service struct {
	settings SettingsSource
	now      func() time.Time
	logger   *slog.Logger
	wake     chan struct{}

	mu        sync.Mutex
	obs       observation
	cfg       Settings
	cfgLoaded bool
	cfgStale  bool
	firstSeen map[string]int
	pass      int
	client    func(url, token string) *Client

	loadMu    sync.Mutex
	snapshots singleflight
}

// NewService returns a service reading settings from src. clientFor builds the
// gateway client for an address and token; nil uses the default HTTP client.
func NewService(src SettingsSource, clientFor func(url, token string) *Client, now func() time.Time, logger *slog.Logger) *Service {
	if clientFor == nil {
		clientFor = func(u, t string) *Client { return NewClient(u, t, nil) }
	}
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{settings: src, client: clientFor, now: now, logger: logger, wake: make(chan struct{}, 1), firstSeen: map[string]int{}}
}

// gatewayID names the address and token an observation belongs to without keeping the token.
func gatewayID(cfg Settings) string {
	if cfg.GatewayURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cfg.GatewayURL + "\n" + cfg.Token))
	return hex.EncodeToString(sum[:])
}

// loadSettings reads the settings and caches them.
func (s *Service) loadSettings(ctx context.Context) (Settings, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	s.mu.Lock()
	s.cfgStale = false
	s.mu.Unlock()
	cfg, err := s.settings.Load(ctx)
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.cfg, s.cfgLoaded = cfg, true
	s.mu.Unlock()
	return cfg, nil
}

// cachedSettings returns the settings from the last load. It reloads only when
// there is none yet or a configuration change has been announced, and falls back
// to the last good copy when that reload fails.
func (s *Service) cachedSettings(ctx context.Context) (Settings, error) {
	s.mu.Lock()
	cfg, ok, stale := s.cfg, s.cfgLoaded, s.cfgStale
	s.mu.Unlock()
	if ok && !stale {
		return cfg, nil
	}
	loaded, err := s.loadSettings(ctx)
	if err == nil {
		return loaded, nil
	}
	if ok {
		s.logger.Warn("preview: could not refresh settings", "error", err)
		return cfg, nil
	}
	return Settings{}, err
}

// Notify asks the run loop to reconcile now and marks the cached settings out of date. It never blocks.
func (s *Service) Notify() {
	s.mu.Lock()
	s.cfgStale = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run reconciles on every Notify and every [ReconcileInterval] until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(ReconcileInterval)
	defer t.Stop()
	for {
		s.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

// handAdded maps each enabled hand-added NDI name to its source. The first by id wins a repeated name.
func handAdded(cfg Settings) map[string]NDISource {
	out := make(map[string]NDISource, len(cfg.NDISources))
	for _, src := range cfg.NDISources {
		if _, dup := out[src.Name]; src.Enabled && !dup {
			out[src.Name] = src
		}
	}
	return out
}

func desiredStreams(cfg Settings, sources []Source) []Stream {
	out := make([]Stream, 0, len(sources)+len(cfg.Cameras)+len(cfg.NDISources))
	manual := handAdded(cfg)
	for _, src := range sources {
		if _, ok := manual[src.Name]; !ok {
			out = append(out, Stream{Key: KeyPrefixNDI + src.Name, Kind: "ndi", NDIName: src.Name})
		}
	}
	for name, src := range manual {
		out = append(out, Stream{Key: KeyPrefixNDI + name, Kind: "ndi", NDIName: name, NDIAddress: src.Address})
	}
	for _, cam := range cfg.Cameras {
		if cam.Enabled && cam.URL != "" {
			out = append(out, Stream{Key: KeyPrefixCamera + cam.ID, Kind: "rtsp", URL: cam.URL})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Reconcile sends the gateway the full desired stream set and records what it reports.
func (s *Service) Reconcile(ctx context.Context) {
	cfg, err := s.loadSettings(ctx)
	if err != nil {
		s.logger.Warn("preview: could not read settings", "error", err)
		return
	}
	now := s.now()
	id := gatewayID(cfg)
	if cfg.GatewayURL == "" {
		s.record(cfg, observation{status: GatewayStatus{State: GatewayNotConfigured, Message: msgNotConfigured, ObservedAt: now}})
		return
	}
	c := s.client(cfg.GatewayURL, cfg.Token)
	prev := s.observationFor(id)

	fail := func(err error) {
		if prev.status.State != GatewayUnreachable {
			s.logger.Warn("preview: gateway unreachable", "error", logSafe(err))
		}
		s.record(cfg, observation{
			gateway: id,
			status:  GatewayStatus{State: GatewayUnreachable, Message: msgUnreachable, ObservedAt: now},
			sources: prev.sources,
		})
	}
	sources, err := c.Sources(ctx)
	if err != nil {
		fail(err)
		return
	}
	camerasRefused := false
	if err := c.PutStreams(ctx, desiredStreams(cfg, sources)); err != nil {
		if !isRefusal(err) {
			fail(err)
			return
		}
		camerasRefused = true
		if prev.status.State != GatewayReachable || !prev.camerasRefused {
			s.logger.Warn("preview: gateway refused the stream set", "error", logSafe(err))
		}
		if err := c.PutStreams(ctx, desiredStreams(Settings{}, sources)); err != nil {
			fail(err)
			return
		}
	}
	states, err := c.Streams(ctx)
	if err != nil {
		fail(err)
		return
	}
	byKey := make(map[string]StreamState, len(states))
	for _, st := range states {
		byKey[st.Key] = st
	}
	s.record(cfg, observation{
		gateway: id,
		status:  GatewayStatus{State: GatewayReachable, ObservedAt: now},
		sources: sources,
		streams: byKey,

		camerasRefused: camerasRefused,
	})
}

// isRefusal reports a client error from the gateway that is not a credential
// problem, meaning the gateway is up and rejected what it was sent.
func isRefusal(err error) bool {
	var ge *GatewayError
	return errors.As(err, &ge) && ge.Status >= 400 && ge.Status < 500 &&
		ge.Status != http.StatusUnauthorized && ge.Status != http.StatusForbidden
}

// logSafe drops the gateway's reply text, which may echo a stream address.
func logSafe(err error) any {
	var ge *GatewayError
	if errors.As(err, &ge) {
		return fmt.Sprintf("preview gateway answered %d", ge.Status)
	}
	return err
}

// record stores o and notes which tiles it shows for the first time.
func (s *Service) record(cfg Settings, o observation) {
	s.mu.Lock()
	s.obs = o
	s.mu.Unlock()
	keys := s.tiles(cfg, o)
	s.mu.Lock()
	defer s.mu.Unlock()
	pass := 0
	for _, t := range keys {
		if _, seen := s.firstSeen[t.Key]; !seen {
			if pass == 0 {
				s.pass++
				pass = s.pass
			}
			s.firstSeen[t.Key] = pass
		}
	}
}

// observationFor returns the stored observation when it was made for the gateway
// id, and an empty one otherwise.
func (s *Service) observationFor(id string) observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.obs.gateway != id {
		return observation{}
	}
	return s.obs
}

// currentObservation is what the service knows about the configured gateway.
func (s *Service) currentObservation(cfg Settings) observation {
	if cfg.GatewayURL == "" {
		return observation{status: GatewayStatus{State: GatewayNotConfigured, Message: msgNotConfigured, ObservedAt: s.now()}}
	}
	o := s.observationFor(gatewayID(cfg))
	if o.status.State == "" || o.status.State == GatewayNotConfigured {
		return observation{gateway: gatewayID(cfg), status: GatewayStatus{State: GatewayConnecting, Message: msgConnecting, ObservedAt: s.now()}}
	}
	return o
}

var ndiNamePattern = regexp.MustCompile(`^(.*) \(([^()]*)\)$`)

func labelNDI(name string, surfaces []Surface, nodes []Node) (label, nodeID, surfaceID string) {
	m := ndiNamePattern.FindStringSubmatch(name)
	if m == nil {
		return name, "", ""
	}
	host, source := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	var matches []Surface
	for _, sf := range surfaces {
		if strings.EqualFold(strings.TrimSpace(sf.SourceName), source) {
			matches = append(matches, sf)
		}
	}
	if len(matches) > 1 {
		var byHost []Surface
		for _, sf := range matches {
			if nodeHasName(nodes, sf.NodeID, host) {
				byHost = append(byHost, sf)
			}
		}
		matches = byHost
	}
	if len(matches) != 1 {
		return name, "", ""
	}
	sf := matches[0]
	return sf.NodeID + " · " + sf.SurfaceID, sf.NodeID, sf.SurfaceID
}

func nodeHasName(nodes []Node, nodeID, host string) bool {
	if strings.EqualFold(nodeID, host) {
		return true
	}
	for _, n := range nodes {
		if n.ID != nodeID {
			continue
		}
		for _, name := range n.Names {
			if strings.EqualFold(name, host) {
				return true
			}
		}
	}
	return false
}

func (s *Service) tiles(cfg Settings, o observation) []Tile {
	at := o.status.ObservedAt
	reachable := o.status.State == GatewayReachable
	connecting := o.status.State == GatewayConnecting
	announced := make(map[string]bool, len(o.sources))
	byKey := map[string]Tile{}

	manual := handAdded(cfg)
	ndiTile := func(name string) Tile {
		label, nodeID, surfaceID := labelNDI(name, cfg.Surfaces, cfg.Nodes)
		if src, ok := manual[name]; ok && nodeID == "" && strings.TrimSpace(src.Label) != "" {
			label = src.Label
		}
		return Tile{Key: KeyPrefixNDI + name, Kind: KindNDI, Label: label, NodeID: nodeID, SurfaceID: surfaceID, ObservedAt: at}
	}
	for _, src := range o.sources {
		announced[src.Name] = true
		t := ndiTile(src.Name)
		applyStreamState(&t, o, msgNDIGone)
		byKey[t.Key] = t
	}
	for name := range manual {
		if announced[name] {
			continue
		}
		t := ndiTile(name)
		applyStreamState(&t, o, msgNDIGone)
		byKey[t.Key] = t
	}
	for _, src := range cfg.NDISources {
		key := KeyPrefixNDI + src.Name
		if _, shown := byKey[key]; shown || src.Enabled {
			continue
		}
		t := ndiTile(src.Name)
		if strings.TrimSpace(src.Label) != "" && t.NodeID == "" {
			t.Label = src.Label
		}
		t.State, t.Message = TileDisabled, msgNDIDisabled
		byKey[key] = t
	}
	for _, key := range append(append([]string{}, cfg.Layout.Order...), cfg.Layout.Hidden...) {
		name, ok := strings.CutPrefix(key, KeyPrefixNDI)
		if _, shown := byKey[key]; !ok || shown {
			continue
		}
		t := ndiTile(name)
		t.State, t.Message = TileNotFound, msgNDIGone
		if connecting {
			t.State, t.Message = TileConnecting, ""
		} else if !reachable {
			t.State, t.Message = TileError, o.status.Message
		}
		byKey[t.Key] = t
	}
	for _, cam := range cfg.Cameras {
		t := Tile{Key: KeyPrefixCamera + cam.ID, Kind: KindCamera, Label: cam.Name, CameraID: cam.ID, ObservedAt: at}
		if !cam.Enabled {
			t.State, t.Message = TileDisabled, msgDisabled
		} else {
			applyStreamState(&t, o, msgCameraGone)
			if reachable && o.camerasRefused && cam.URL != "" {
				t.State, t.Message = TileError, msgCameraRefused
			}
		}
		byKey[t.Key] = t
	}
	if o.status.State == GatewayNotConfigured {
		return []Tile{}
	}

	hidden := make(map[string]bool, len(cfg.Layout.Hidden))
	for _, k := range cfg.Layout.Hidden {
		hidden[k] = true
	}
	out := make([]Tile, 0, len(byKey))
	placed := map[string]bool{}
	for _, k := range cfg.Layout.Order {
		if t, ok := byKey[k]; ok && !placed[k] {
			placed[k] = true
			out = append(out, t)
		}
	}
	rest := make([]string, 0, len(byKey))
	for k := range byKey {
		if !placed[k] {
			rest = append(rest, k)
		}
	}
	s.mu.Lock()
	seen := make(map[string]int, len(rest))
	for _, k := range rest {
		seen[k] = s.firstSeen[k]
	}
	s.mu.Unlock()
	// A tile not seen yet sorts after every tile that was.
	sort.Slice(rest, func(i, j int) bool {
		a, b := seen[rest[i]], seen[rest[j]]
		if (a == 0) != (b == 0) {
			return b == 0
		}
		if a != b {
			return a < b
		}
		return rest[i] < rest[j]
	})
	for _, k := range rest {
		out = append(out, byKey[k])
	}
	for i := range out {
		out[i].Hidden = hidden[out[i].Key]
	}
	return out
}

func applyStreamState(t *Tile, o observation, goneMsg string) {
	switch o.status.State {
	case GatewayReachable:
	case GatewayConnecting:
		t.State = TileConnecting
		return
	default:
		t.State, t.Message = TileError, msgUnreachable
		return
	}
	st, ok := o.streams[t.Key]
	if !ok {
		t.State = TileConnecting
		return
	}
	t.State, t.Message = st.State, ""
	switch st.State {
	case TileLive, TileIdle, TileConnecting:
	case TileNotFound:
		t.Message = goneMsg
	case TileError:
		t.Message = msgTileError
	default:
		t.State, t.Message = TileError, msgTileError
	}
}

// Overview returns the gateway state and every tile from the latest reconcile
// and the settings loaded with it.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	cfg, err := s.cachedSettings(ctx)
	if err != nil {
		return Overview{}, err
	}
	o := s.currentObservation(cfg)
	return Overview{Gateway: o.status, Tiles: s.tiles(cfg, o)}, nil
}

func (s *Service) gatewayFor(ctx context.Context, key string) (*Client, string, error) {
	cfg, err := s.cachedSettings(ctx)
	if err != nil {
		return nil, "", err
	}
	if cfg.GatewayURL == "" {
		return nil, "", &UnavailableError{Message: msgNotConfigured}
	}
	o := s.currentObservation(cfg)
	if o.status.State == GatewayConnecting {
		return nil, "", &UnavailableError{Message: msgConnecting}
	}
	found := false
	for _, t := range s.tiles(cfg, o) {
		if t.Key == key {
			found = true
			break
		}
	}
	if !found {
		return nil, "", ErrUnknownKey
	}
	return s.client(cfg.GatewayURL, cfg.Token), gatewayID(cfg), nil
}

func (s *Service) wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrGatewayTimeout) {
		return &UnavailableError{Message: msgSlowStart}
	}
	var ge *GatewayError
	if errors.As(err, &ge) && ge.Status >= 400 && ge.Status < 500 && ge.Status != http.StatusUnauthorized && ge.Status != http.StatusForbidden {
		return &UnavailableError{Message: "The preview gateway could not serve this tile. Check the tile's state and try again."}
	}
	return &UnavailableError{Message: msgUnreachable}
}

// Offer forwards a browser's SDP offer for one tile and returns the gateway's answer.
func (s *Service) Offer(ctx context.Context, key string, offer []byte) ([]byte, error) {
	c, _, err := s.gatewayFor(ctx, key)
	if err != nil {
		return nil, err
	}
	answer, err := c.Offer(ctx, key, offer)
	return answer, s.wrap(err)
}

// Snapshot returns the latest JPEG frame of one tile.
func (s *Service) Snapshot(ctx context.Context, key string) ([]byte, error) {
	c, id, err := s.gatewayFor(ctx, key)
	if err != nil {
		return nil, err
	}
	frame, err := s.snapshots.do(id+"|"+key, func() ([]byte, error) {
		return c.Snapshot(context.WithoutCancel(ctx), key)
	})
	return frame, s.wrap(err)
}
