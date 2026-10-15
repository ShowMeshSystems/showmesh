package preview

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ReconcileInterval = 10 * time.Second

	GatewayNotConfigured = "not_configured"
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
	msgUnreachable   = "The preview gateway cannot be reached. Check that it is running and that its address and token are right."
	msgNDIGone       = "This source is no longer announced on the network. Check that its sender is running."
	msgCameraGone    = "The camera did not answer. Check its address and that it is on."
	msgDisabled      = "This camera is turned off. Turn it on to see it here."
	msgTileError     = "The preview could not start. Check the gateway log."
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
	status  GatewayStatus
	sources []Source
	streams map[string]StreamState
}

// Service reconciles the gateway's stream set and answers the dashboard.
type Service struct {
	settings SettingsSource
	now      func() time.Time
	logger   *slog.Logger
	wake     chan struct{}

	mu     sync.Mutex
	obs    observation
	client func(url, token string) *Client
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
	return &Service{settings: src, client: clientFor, now: now, logger: logger, wake: make(chan struct{}, 1)}
}

// Notify asks the run loop to reconcile now. It never blocks.
func (s *Service) Notify() {
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

func desiredStreams(cfg Settings, sources []Source) []Stream {
	out := make([]Stream, 0, len(sources)+len(cfg.Cameras))
	for _, src := range sources {
		out = append(out, Stream{Key: KeyPrefixNDI + src.Name, Kind: "ndi", NDIName: src.Name})
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
	cfg, err := s.settings.Load(ctx)
	if err != nil {
		s.logger.Warn("preview: could not read settings", "error", err)
		return
	}
	now := s.now()
	if cfg.GatewayURL == "" {
		s.store(observation{status: GatewayStatus{State: GatewayNotConfigured, Message: msgNotConfigured, ObservedAt: now}})
		return
	}
	c := s.client(cfg.GatewayURL, cfg.Token)
	prev := s.snapshot()

	fail := func(err error) {
		s.logger.Warn("preview: gateway reconcile failed", "error", err)
		s.store(observation{
			status:  GatewayStatus{State: GatewayUnreachable, Message: msgUnreachable, ObservedAt: now},
			sources: prev.sources,
		})
	}
	sources, err := c.Sources(ctx)
	if err != nil {
		fail(err)
		return
	}
	if err := c.PutStreams(ctx, desiredStreams(cfg, sources)); err != nil {
		fail(err)
		return
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
	s.store(observation{
		status:  GatewayStatus{State: GatewayReachable, ObservedAt: now},
		sources: sources,
		streams: byKey,
	})
}

func (s *Service) store(o observation) {
	s.mu.Lock()
	s.obs = o
	s.mu.Unlock()
}

func (s *Service) snapshot() observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.obs
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
	announced := make(map[string]bool, len(o.sources))
	byKey := map[string]Tile{}

	ndiTile := func(name string) Tile {
		label, nodeID, surfaceID := labelNDI(name, cfg.Surfaces, cfg.Nodes)
		return Tile{Key: KeyPrefixNDI + name, Kind: KindNDI, Label: label, NodeID: nodeID, SurfaceID: surfaceID, ObservedAt: at}
	}
	for _, src := range o.sources {
		announced[src.Name] = true
		t := ndiTile(src.Name)
		applyStreamState(&t, o.streams, reachable, msgNDIGone)
		byKey[t.Key] = t
	}
	for _, key := range append(append([]string{}, cfg.Layout.Order...), cfg.Layout.Hidden...) {
		name, ok := strings.CutPrefix(key, KeyPrefixNDI)
		if !ok || announced[name] {
			continue
		}
		t := ndiTile(name)
		t.State, t.Message = TileNotFound, msgNDIGone
		if !reachable {
			t.State, t.Message = TileError, o.status.Message
		}
		byKey[t.Key] = t
	}
	for _, cam := range cfg.Cameras {
		t := Tile{Key: KeyPrefixCamera + cam.ID, Kind: KindCamera, Label: cam.Name, CameraID: cam.ID, ObservedAt: at}
		if !cam.Enabled {
			t.State, t.Message = TileDisabled, msgDisabled
		} else {
			applyStreamState(&t, o.streams, reachable, msgCameraGone)
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
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, byKey[k])
	}
	for i := range out {
		out[i].Hidden = hidden[out[i].Key]
	}
	return out
}

func applyStreamState(t *Tile, streams map[string]StreamState, reachable bool, goneMsg string) {
	if !reachable {
		t.State, t.Message = TileError, msgUnreachable
		return
	}
	st, ok := streams[t.Key]
	if !ok {
		t.State = TileConnecting
		return
	}
	t.State, t.Message = st.State, st.Message
	switch st.State {
	case TileLive, TileIdle, TileConnecting:
		t.Message = ""
	case TileNotFound:
		t.Message = goneMsg
	case TileError:
		if t.Message == "" {
			t.Message = msgTileError
		}
	default:
		t.State, t.Message = TileError, msgTileError
	}
}

// Overview returns the gateway state and every tile from the latest reconcile
// and the current settings.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	cfg, err := s.settings.Load(ctx)
	if err != nil {
		return Overview{}, err
	}
	o := s.snapshot()
	if cfg.GatewayURL == "" {
		o = observation{status: GatewayStatus{State: GatewayNotConfigured, Message: msgNotConfigured, ObservedAt: s.now()}}
	} else if o.status.State == "" || o.status.State == GatewayNotConfigured {
		o = observation{status: GatewayStatus{State: GatewayUnreachable, Message: msgUnreachable, ObservedAt: s.now()}}
	}
	return Overview{Gateway: o.status, Tiles: s.tiles(cfg, o)}, nil
}

func (s *Service) gatewayFor(ctx context.Context, key string) (*Client, error) {
	cfg, err := s.settings.Load(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.GatewayURL == "" {
		return nil, &UnavailableError{Message: msgNotConfigured}
	}
	o := s.snapshot()
	if o.status.State == "" || o.status.State == GatewayNotConfigured {
		o = observation{status: GatewayStatus{State: GatewayUnreachable}}
	}
	found := false
	for _, t := range s.tiles(cfg, o) {
		if t.Key == key {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrUnknownKey
	}
	return s.client(cfg.GatewayURL, cfg.Token), nil
}

func (s *Service) wrap(err error) error {
	if err == nil {
		return nil
	}
	if isGatewayReply(err) {
		return &UnavailableError{Message: "The preview gateway could not serve this tile. Check the tile's state and try again."}
	}
	return &UnavailableError{Message: msgUnreachable}
}

// Offer forwards a browser's SDP offer for one tile and returns the gateway's answer.
func (s *Service) Offer(ctx context.Context, key string, offer []byte) ([]byte, error) {
	c, err := s.gatewayFor(ctx, key)
	if err != nil {
		return nil, err
	}
	answer, err := c.Offer(ctx, key, offer)
	return answer, s.wrap(err)
}

// Snapshot returns the latest JPEG frame of one tile.
func (s *Service) Snapshot(ctx context.Context, key string) ([]byte, error) {
	c, err := s.gatewayFor(ctx, key)
	if err != nil {
		return nil, err
	}
	frame, err := c.Snapshot(ctx, key)
	return frame, s.wrap(err)
}
