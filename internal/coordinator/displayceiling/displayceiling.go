// Package displayceiling keeps every FPP in fpp.endpoints at the display's one
// brightness ceiling (ADR-062), under the per-poll rules of
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.8. It is fed each FPP's
// brightness poll as a Reading and writes through the plugin's
// "ShowMesh: Set Brightness Ceiling" FPP command.
package displayceiling

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// SetCeilingCommand is the FPP command the plugin registers. It travels verbatim.
const SetCeilingCommand = "ShowMesh: Set Brightness Ceiling"

// Who set the desired ceiling.
const (
	ChangedByOperator = "operator"
	ChangedByFPP      = "fpp"
)

// MaxFadeSeconds is the plugin's own fade bound.
const MaxFadeSeconds = 86400

// ownWriteSlack is how far past a write's completion an FPP's reported change
// time may fall and still be read as that write landing, not a deliberate change.
const ownWriteSlack = 2 * time.Second

// State is how one FPP stands against the desired ceiling.
type State string

const (
	StateFollowing    State = "following"
	StateCatchingUp   State = "catchingUp"
	StateNotReporting State = "notReporting"
	StatePluginTooOld State = "pluginTooOld"
)

// Store persists the desired ceiling. *store.Store satisfies it.
type Store interface {
	GetDisplayCeiling(ctx context.Context) (store.DisplayCeilingRecord, bool, error)
	SetDisplayCeiling(ctx context.Context, rec store.DisplayCeilingRecord) error
}

// DefaultPollInterval is the brightness poll's cadence when Options gives none.
const DefaultPollInterval = 5 * time.Second

// Reading is one brightness poll of one FPP (contract section 2.6). Err is set
// when the poll failed and NotFound when the host answered 404. The ceiling
// fields are absent from a plugin older than section 2.8.
type Reading struct {
	InstanceID string
	ReceivedAt time.Time
	NotFound   bool
	Err        error

	Ceiling                *int64
	FadeActive             *bool
	UpdatedAtMillis        *int64
	CeilingTarget          *int64
	CeilingFadeEndMillis   *int64
	CeilingChangedAtMillis *int64
}

// Endpoint is one configured FPP.
type Endpoint struct{ ID, URL string }

// Writer sends the ceiling command to one FPP.
type Writer func(ctx context.Context, baseURL string, targetPercent, fadeSeconds int) error

// Options configures a Reconciler. Endpoints and Store are required.
type Options struct {
	Store        Store
	Endpoints    func(ctx context.Context) ([]Endpoint, error)
	Write        Writer
	Now          func() time.Time
	PollInterval time.Duration
	TickInterval time.Duration
	WriteTimeout time.Duration
	Logger       *slog.Logger
}

// FPPStatus is one FPP's place against the desired ceiling.
type FPPStatus struct {
	InstanceID    string
	State         State
	Reason        string
	Ceiling       *int
	CeilingTarget *int
	ReportedAt    time.Time
}

// Snapshot is the desired ceiling, if any, and every configured FPP's status.
type Snapshot struct {
	Desired *store.DisplayCeilingRecord
	FPPs    []FPPStatus
}

type lastWrite struct {
	target    int
	forChange time.Time
	sentAt    time.Time
	doneAt    time.Time
	err       error
}

type fppState struct {
	reading  *Reading
	write    *lastWrite
	inflight bool
}

// Reconciler holds the desired ceiling and brings every FPP to it.
type Reconciler struct {
	opts Options

	mu      sync.Mutex
	loaded  bool
	desired *store.DisplayCeilingRecord
	fpps    map[string]*fppState

	kick chan struct{}
	wg   sync.WaitGroup
}

// New builds a Reconciler. Zero durations take the collector's poll interval,
// one fifth of it for the tick, and the FPP command client's timeout.
func New(opts Options) *Reconciler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = opts.PollInterval / 5
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = fppcommand.DefaultTimeout
	}
	if opts.Write == nil {
		opts.Write = WriteCeiling
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Reconciler{opts: opts, fpps: map[string]*fppState{}, kick: make(chan struct{}, 1)}
}

// WriteCeiling invokes the plugin's ceiling command on one FPP.
func WriteCeiling(ctx context.Context, baseURL string, targetPercent, fadeSeconds int) error {
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return fmt.Errorf("displayceiling: building the fpp client: %w", err)
	}
	_, err = client.Invoke(ctx, SetCeilingCommand, []string{strconv.Itoa(targetPercent), strconv.Itoa(fadeSeconds)})
	return err
}

// Observe records one poll's reading. The collector calls it on every poll.
func (r *Reconciler) Observe(reading Reading) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.fpps[reading.InstanceID]
	if st == nil {
		st = &fppState{}
		r.fpps[reading.InstanceID] = st
	}
	rd := reading
	st.reading = &rd
}

// Kick asks for a reconcile pass now instead of at the next tick.
func (r *Reconciler) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run reconciles on every tick and kick until ctx ends, then waits for writes.
func (r *Reconciler) Run(ctx context.Context) {
	t := time.NewTicker(r.opts.TickInterval)
	defer t.Stop()
	defer r.wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
		}
		r.Step(ctx)
	}
}

// Set makes target the desired ceiling, fading over fadeSeconds, and kicks a pass.
func (r *Reconciler) Set(ctx context.Context, target, fadeSeconds int, byKind, by, byName string) (store.DisplayCeilingRecord, error) {
	if target < 0 || target > 100 {
		return store.DisplayCeilingRecord{}, fmt.Errorf("displayceiling: target %d is outside 0 to 100", target)
	}
	if fadeSeconds < 0 || fadeSeconds > MaxFadeSeconds {
		return store.DisplayCeilingRecord{}, fmt.Errorf("displayceiling: fade %d seconds is outside 0 to %d", fadeSeconds, MaxFadeSeconds)
	}
	now := r.opts.Now()
	rec := store.DisplayCeilingRecord{TargetPercent: target, ChangedAt: now, ChangedByKind: byKind, ChangedBy: by, ChangedByName: byName}
	if fadeSeconds > 0 {
		rec.FadeEndsAt = now.Add(time.Duration(fadeSeconds) * time.Second)
	}
	r.mu.Lock()
	err := r.opts.Store.SetDisplayCeiling(ctx, rec)
	if err == nil {
		r.loaded, r.desired = true, &rec
	}
	r.mu.Unlock()
	if err != nil {
		return store.DisplayCeilingRecord{}, err
	}
	r.Kick()
	return rec, nil
}

// loadLocked reads the desired ceiling once. Callers hold r.mu.
func (r *Reconciler) loadLocked(ctx context.Context) error {
	if r.loaded {
		return nil
	}
	rec, ok, err := r.opts.Store.GetDisplayCeiling(ctx)
	if err != nil {
		return err
	}
	r.loaded = true
	if ok {
		r.desired = &rec
	}
	return nil
}

// correctedChangeTime is the FPP's last deliberate change on the
// coordinator's clock, and whether the reading carries one (rule 1).
func correctedChangeTime(rd *Reading) (time.Time, int64, bool) {
	if rd.CeilingChangedAtMillis == nil || *rd.CeilingChangedAtMillis <= 0 || rd.UpdatedAtMillis == nil || rd.CeilingTarget == nil {
		return time.Time{}, 0, false
	}
	offset := rd.ReceivedAt.UnixMilli() - *rd.UpdatedAtMillis
	return time.UnixMilli(*rd.CeilingChangedAtMillis + offset).UTC(), offset, true
}

// reportedTarget is where the FPP's ceiling is going, or false when an old
// plugin is mid-fade and its target cannot be known (rule 3).
func reportedTarget(rd *Reading) (int, bool) {
	if rd.CeilingTarget != nil {
		return int(*rd.CeilingTarget), true
	}
	if rd.Ceiling != nil && rd.FadeActive != nil && !*rd.FadeActive {
		return int(*rd.Ceiling), true
	}
	return 0, false
}

func usable(rd *Reading) bool {
	return rd != nil && rd.Err == nil && !rd.NotFound
}

// Step runs one reconcile pass: adoption across the round, then writes.
func (r *Reconciler) Step(ctx context.Context) {
	endpoints, err := r.opts.Endpoints(ctx)
	if err != nil {
		r.opts.Logger.Warn("display ceiling: listing fpp endpoints failed", "error", err)
		return
	}
	now := r.opts.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLocked(ctx); err != nil {
		r.opts.Logger.Warn("display ceiling: reading the desired ceiling failed", "error", err)
		return
	}
	inScope := map[string]bool{}
	for _, ep := range endpoints {
		inScope[ep.ID] = true
	}
	for id := range r.fpps {
		if !inScope[id] {
			delete(r.fpps, id)
		}
	}

	r.adoptLocked(ctx, endpoints)
	if r.desired == nil {
		return
	}
	for _, ep := range endpoints {
		st := r.fpps[ep.ID]
		if st == nil || st.inflight || !usable(st.reading) {
			continue
		}
		got, known := reportedTarget(st.reading)
		if !known || got == r.desired.TargetPercent {
			continue
		}
		if w := st.write; w != nil && w.forChange.Equal(r.desired.ChangedAt) {
			if now.Sub(w.sentAt) < r.opts.PollInterval || !st.reading.ReceivedAt.After(w.doneAt) {
				continue
			}
		}
		r.launchLocked(ctx, ep, st, *r.desired, now)
	}
}

// adoptLocked applies rule 2: the latest deliberate FPP change newer than the
// desired ceiling becomes the desired ceiling. Callers hold r.mu.
func (r *Reconciler) adoptLocked(ctx context.Context, endpoints []Endpoint) {
	var (
		best      *Reading
		bestAt    time.Time
		bestShift int64
	)
	for _, ep := range endpoints {
		st := r.fpps[ep.ID]
		if st == nil || !usable(st.reading) {
			continue
		}
		rd := st.reading
		changedAt, offset, ok := correctedChangeTime(rd)
		if !ok {
			continue
		}
		target := int(*rd.CeilingTarget)
		if r.desired != nil && (target == r.desired.TargetPercent || !changedAt.After(r.desired.ChangedAt)) {
			continue
		}
		if w := st.write; w != nil && w.err == nil && w.target == target && !changedAt.After(w.doneAt.Add(ownWriteSlack)) {
			continue
		}
		if best == nil || changedAt.After(bestAt) {
			best, bestAt, bestShift = rd, changedAt, offset
		}
	}
	if best == nil {
		return
	}
	rec := store.DisplayCeilingRecord{
		TargetPercent: int(*best.CeilingTarget), ChangedAt: bestAt,
		ChangedByKind: ChangedByFPP, ChangedBy: best.InstanceID,
	}
	if best.CeilingFadeEndMillis != nil && *best.CeilingFadeEndMillis > 0 {
		rec.FadeEndsAt = time.UnixMilli(*best.CeilingFadeEndMillis + bestShift).UTC()
	}
	if err := r.opts.Store.SetDisplayCeiling(ctx, rec); err != nil {
		r.opts.Logger.Warn("display ceiling: storing an adopted ceiling failed", "instanceId", best.InstanceID, "error", err)
		return
	}
	r.opts.Logger.Info("display ceiling: adopted a change made on an FPP", "instanceId", best.InstanceID, "targetPercent", rec.TargetPercent)
	r.desired = &rec
}

// remainingFadeSeconds is the desired fade's remaining whole seconds, rounded up.
func remainingFadeSeconds(desired store.DisplayCeilingRecord, now time.Time) int {
	if desired.FadeEndsAt.IsZero() || !desired.FadeEndsAt.After(now) {
		return 0
	}
	left := desired.FadeEndsAt.Sub(now)
	secs := int((left + time.Second - 1) / time.Second)
	if secs > MaxFadeSeconds {
		secs = MaxFadeSeconds
	}
	return secs
}

// launchLocked starts one write without waiting for it. Callers hold r.mu.
func (r *Reconciler) launchLocked(ctx context.Context, ep Endpoint, st *fppState, desired store.DisplayCeilingRecord, now time.Time) {
	fade := remainingFadeSeconds(desired, now)
	st.inflight = true
	st.write = &lastWrite{target: desired.TargetPercent, forChange: desired.ChangedAt, sentAt: now}
	w := st.write
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.opts.WriteTimeout)
		err := r.opts.Write(wctx, ep.URL, desired.TargetPercent, fade)
		cancel()
		if err != nil {
			r.opts.Logger.Warn("display ceiling: writing an fpp failed", "instanceId", ep.ID, "targetPercent", desired.TargetPercent, "error", err)
		}
		r.mu.Lock()
		w.doneAt, w.err = r.opts.Now(), err
		st.inflight = false
		r.mu.Unlock()
	}()
}

// Snapshot reports the desired ceiling and every configured FPP's status.
func (r *Reconciler) Snapshot(ctx context.Context) (Snapshot, error) {
	endpoints, err := r.opts.Endpoints(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	now := r.opts.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLocked(ctx); err != nil {
		return Snapshot{}, err
	}
	var out Snapshot
	if r.desired != nil {
		d := *r.desired
		out.Desired = &d
	}
	ids := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		ids = append(ids, ep.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out.FPPs = append(out.FPPs, r.statusLocked(id, now))
	}
	return out, nil
}

func (r *Reconciler) statusLocked(id string, now time.Time) FPPStatus {
	s := FPPStatus{InstanceID: id}
	st := r.fpps[id]
	var rd *Reading
	if st != nil {
		rd = st.reading
	}
	staleAfter := 3 * r.opts.PollInterval
	switch {
	case rd == nil:
		s.State, s.Reason = StateNotReporting, fmt.Sprintf("The player %s has not reported its brightness yet. Check that it is on and reachable.", id)
		return s
	case rd.NotFound:
		s.State, s.Reason = StateNotReporting, fmt.Sprintf("The player %s does not report brightness, so it cannot follow the display brightness. Install or update its ShowMesh plugin.", id)
		return s
	case rd.Err != nil:
		s.State, s.Reason = StateNotReporting, fmt.Sprintf("The brightness on the player %s could not be read. Check that it is on and reachable.", id)
		return s
	case now.Sub(rd.ReceivedAt) > staleAfter:
		s.State, s.Reason = StateNotReporting, fmt.Sprintf("The player %s has not reported its brightness for %d seconds. Check that it is on and reachable.", id, int(now.Sub(rd.ReceivedAt)/time.Second))
		return s
	}
	s.ReportedAt = rd.ReceivedAt
	if rd.Ceiling != nil {
		v := int(*rd.Ceiling)
		s.Ceiling = &v
	}
	if rd.CeilingTarget != nil {
		v := int(*rd.CeilingTarget)
		s.CeilingTarget = &v
	}
	if r.desired != nil {
		want := r.desired.TargetPercent
		got, known := reportedTarget(rd)
		if !known || got != want {
			at := "fading"
			if known {
				at = fmt.Sprintf("at %d%%", got)
			}
			s.State = StateCatchingUp
			if st.write != nil && st.write.err != nil && st.write.target == want {
				s.Reason = fmt.Sprintf("The player %s is %s and could not be set to %d%%. Check that it is reachable.", id, at, want)
			} else {
				s.Reason = fmt.Sprintf("The player %s is %s and is being set to %d%%.", id, at, want)
			}
			return s
		}
	}
	if rd.CeilingChangedAtMillis == nil {
		s.State, s.Reason = StatePluginTooOld, fmt.Sprintf("The player %s follows the display brightness, but a change made on it reaches no other player. Update its ShowMesh plugin.", id)
		return s
	}
	s.State = StateFollowing
	return s
}
