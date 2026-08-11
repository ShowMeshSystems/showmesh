package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// MaxEventDetailsBytes bounds [EventRecord.Details]. ARCHITECTURE section
// 11 names disk exhaustion from an untrusted, unbounded input as a
// concrete threat this project must design against, and an event's Details
// is exactly that shape of input: it is free-form JSON a caller attaches
// to an event it is recording, and nothing upstream of [Store.AppendEvent]
// currently caps its size. 8 KiB is a SHOWMESH HYPOTHESIS — generous
// enough for a structured summary of "what changed and to what" (a few
// dozen key/value pairs), nowhere near enough to let one malformed or
// hostile caller turn the append-only events table into a disk-exhaustion
// vector one event at a time.
const MaxEventDetailsBytes = 8 * 1024

// DefaultEventsPageSize and MaxEventsPageSize bound [Store.ListEvents]'s
// limit parameter: DefaultEventsPageSize is used when the caller passes
// limit <= 0, and MaxEventsPageSize is the hard ceiling no limit value can
// exceed, silently clamped rather than rejected — a caller asking for "as
// many as exist" gets a safe bound back rather than an error, which is the
// same posture typed rate limits and paginated APIs generally take. Both
// are SHOWMESH HYPOTHESES sized only as "clearly larger than one page of
// an operator's screen, clearly small enough that one HTTP response body
// stays cheap"; neither is derived from a measured payload size.
const (
	DefaultEventsPageSize = 100
	MaxEventsPageSize     = 500
)

// EventRecord is one entry in the coordinator's append-only event history
// (OBSERVABILITY section 4.3). Category and Severity are stored as
// whatever string the caller provides — this package does not fix their
// vocabulary, the same way [observation.SignalID]'s doc comment explains
// pkg/observation does not fix signal-ID syntax: OBSERVABILITY section
// 11.2 (severity: informational | warning | critical) is Phase O2 and does
// not exist as an enforced type anywhere yet, and inventing one here would
// let this storage package quietly become the place that vocabulary is
// decided instead of OBSERVABILITY or the API layer that renders it.
// AppendEvent only requires both to be non-empty.
type EventRecord struct {
	// Seq is assigned by [Store.AppendEvent] and returned from it; any
	// value set here by a caller building an EventRecord to append is
	// ignored. Only meaningful on a record returned by AppendEvent,
	// ListEvents, or LatestEventSeq.
	Seq int64

	// RecordedAt is the store's own clock at the moment AppendEvent wrote
	// this row — bookkeeping, like [NodeRecord.FirstSeenAt], never
	// evidence of when the underlying change actually happened. A value
	// set here by a caller is ignored; AppendEvent always stamps it.
	RecordedAt time.Time

	// OccurredAt is when the change this event describes actually
	// happened, if known. Nil when the change was first learned from
	// evidence of unknown age (contract section 3.3) — a coordinator
	// restart replaying a retained MQTT delivery is the concrete case:
	// the coordinator can only honestly record "I learned this just now,
	// at RecordedAt", never a fabricated moment the change supposedly
	// occurred.
	OccurredAt *time.Time

	// Source names what produced this event, e.g. "mqtt-inventory",
	// "fpp-rest". Required.
	Source string

	// Resource is what this event is about. Both Kind and ID are
	// required.
	Resource observation.ResourceRef

	// Category and Severity: see the type doc comment above for why
	// neither is a typed enum yet. Both required.
	Category string
	Severity string

	// Summary is a short human-readable description. Required.
	Summary string

	// Details is free-form structured context, e.g. {"from":"online",
	// "to":"offline"}. Nil or empty is stored as the JSON object `{}`
	// (see the wire contract section 6.10's event example, which always
	// renders details as an object, never null) rather than as SQL NULL
	// or an empty string, so a reader never has to special-case "no
	// details" as a third shape alongside "an empty object". Must be
	// valid JSON and at most [MaxEventDetailsBytes] long; see
	// [EventRecord.validate].
	Details json.RawMessage

	// CorrelationID optionally ties this event to others describing the
	// same incident (OBSERVABILITY section 4.4). Nil when this event
	// stands alone.
	CorrelationID *string
}

// Sentinel errors [EventRecord.validate] returns, each wrapped with the
// specific field that failed, matching pkg/observation.Validate's pattern
// of one distinguishable error per rule so a caller can errors.Is against
// the specific one that fired.
var (
	ErrEventMissingSource      = errors.New("store: event Source is empty")
	ErrEventMissingResource    = errors.New("store: event Resource.Kind or Resource.ID is empty")
	ErrEventMissingCategory    = errors.New("store: event Category is empty")
	ErrEventMissingSeverity    = errors.New("store: event Severity is empty")
	ErrEventMissingSummary     = errors.New("store: event Summary is empty")
	ErrEventDetailsTooLarge    = errors.New("store: event Details exceeds MaxEventDetailsBytes")
	ErrEventDetailsInvalidJSON = errors.New("store: event Details is not valid JSON")
)

// validate reports whether e is safe for [Store.AppendEvent] to write. An
// EventRecord that fails this is always a caller bug — the collector or
// inventory code building it skipped a required field, or attached an
// oversized or malformed Details payload — never a condition this package
// silently repairs.
func (e EventRecord) validate() error {
	if e.Source == "" {
		return ErrEventMissingSource
	}
	if e.Resource.Kind == "" || e.Resource.ID == "" {
		return ErrEventMissingResource
	}
	if e.Category == "" {
		return ErrEventMissingCategory
	}
	if e.Severity == "" {
		return ErrEventMissingSeverity
	}
	if e.Summary == "" {
		return ErrEventMissingSummary
	}
	if len(e.Details) > MaxEventDetailsBytes {
		return fmt.Errorf("%w: %d bytes > %d", ErrEventDetailsTooLarge, len(e.Details), MaxEventDetailsBytes)
	}
	if len(e.Details) > 0 && !json.Valid(e.Details) {
		return ErrEventDetailsInvalidJSON
	}
	return nil
}

// AppendEvent validates ev, then records it as the next entry in the
// event history, returning its assigned seq. ev.Seq and ev.RecordedAt are
// ignored on input — see [EventRecord]'s doc comment — and the returned
// EventRecord from [Store.ListEvents] carries the values this method
// actually assigned instead.
//
// The insert and the amortized pruning pass it may trigger (see
// [pruneEvents] in retention.go) share one transaction: either both
// happen or neither does. This is a deliberate choice against the
// alternative of pruning on a background goroutine — a goroutine would
// need its own shutdown coordination (a stop channel, a WaitGroup) purely
// to satisfy ADR-012's "start and stay up, leak nothing" bar for a
// mechanism that a write-coupled design does not need at all: it runs on
// the same goroutine, inside the same transaction, and simply does not run
// when AppendEvent is not called.
//
// That coupling has a real, accepted cost, corrected here after Step 3
// review finding 3.5 caught a previous version of this comment asserting
// it away: a coordinator that appends NO events for a long stretch has, by
// construction, nothing that can trigger a check in that stretch, so
// already-stored events aging past maxEventAge during it are not pruned
// until the next insert, whenever that happens. This is accepted, not
// fixed, because there is genuinely nothing to prune-check for in that
// specific window — no new row exists to justify running a write-coupled
// pass with nothing driving it. What was actually wrong, and what Step 3
// review finding 3.5 is about, is a DIFFERENT case this trigger logic used
// to get wrong: a coordinator that DOES keep appending events, just too
// infrequently for [pruneEveryNEvents] insertions to ever accumulate
// between restarts, went just as unpruned as the "nothing at all" case
// even though it had plenty of opportunities (every insert) to check. See
// [pruneCheckInterval] in retention.go for the second trigger that closes
// that gap: below, a prune pass now runs when EITHER pruneEveryNEvents
// insertions have elapsed OR pruneCheckInterval of wall-clock time has,
// whichever comes first.
func (s *Store) AppendEvent(ctx context.Context, ev EventRecord) (int64, error) {
	if err := ev.validate(); err != nil {
		return 0, fmt.Errorf("store: append event: %w", err)
	}

	details := ev.Details
	if len(details) == 0 {
		details = json.RawMessage("{}")
	}
	var correlationID any
	if ev.CorrelationID != nil {
		correlationID = *ev.CorrelationID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin append event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (
			recorded_at, occurred_at, source, resource_kind, resource_id,
			category, severity, summary, details, correlation_id
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		timeToDB(s.now()), timePtrToDB(ev.OccurredAt), ev.Source,
		string(ev.Resource.Kind), ev.Resource.ID,
		ev.Category, ev.Severity, ev.Summary, string(details), correlationID,
	)
	if err != nil {
		return 0, fmt.Errorf("store: append event: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: append event: read assigned seq: %w", err)
	}

	// Two independent triggers, either sufficient on its own: insert
	// volume (bounds row-count growth correctly on its own, since
	// maxEventRows can only grow via an insert) and elapsed wall-clock
	// time since the last prune pass (bounds event AGE correctly under a
	// low insert rate, which insert-count alone cannot — see
	// pruneEveryNEvents's and pruneCheckInterval's doc comments in
	// retention.go for why both are needed, and this.now().Sub(zero time)
	// below intentionally being "huge" is what makes the very first
	// AppendEvent call after every process start also check, regardless of
	// how far eventAppendCount is from its next multiple of
	// pruneEveryNEvents).
	byCount := s.eventAppendCount.Add(1)%pruneEveryNEvents == 0
	byAge := false
	if !byCount {
		last := s.lastPruneAtNanos.Load()
		byAge = last == 0 || s.now().Sub(time.Unix(0, last)) >= pruneCheckInterval
	}
	if byCount || byAge {
		if err := s.pruneEvents(ctx, tx); err != nil {
			return 0, fmt.Errorf("store: append event: %w", err)
		}
		s.lastPruneAtNanos.Store(s.now().UnixNano())
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit append event: %w", err)
	}
	return seq, nil
}

const eventColumns = `
	seq, recorded_at, occurred_at, source, resource_kind, resource_id,
	category, severity, summary, details, correlation_id
`

func scanEvent(row interface {
	Scan(dest ...any) error
}) (EventRecord, error) {
	var (
		seq                                  int64
		recordedAt                           string
		occurredAt                           sql.NullString
		source, resourceKind, resourceID     string
		category, severity, summary, details string
		correlationID                        sql.NullString
	)
	if err := row.Scan(
		&seq, &recordedAt, &occurredAt, &source, &resourceKind, &resourceID,
		&category, &severity, &summary, &details, &correlationID,
	); err != nil {
		return EventRecord{}, err
	}

	recordedAtTime, err := dbToTime(recordedAt)
	if err != nil {
		return EventRecord{}, fmt.Errorf("store: scan event %d: parse recorded_at: %w", seq, err)
	}
	occurredAtPtr, err := dbToTimePtr(occurredAt)
	if err != nil {
		return EventRecord{}, fmt.Errorf("store: scan event %d: parse occurred_at: %w", seq, err)
	}

	rec := EventRecord{
		Seq:        seq,
		RecordedAt: recordedAtTime,
		OccurredAt: occurredAtPtr,
		Source:     source,
		Resource:   observation.ResourceRef{Kind: observation.ResourceKind(resourceKind), ID: resourceID},
		Category:   category,
		Severity:   severity,
		Summary:    summary,
		Details:    json.RawMessage(details),
	}
	if correlationID.Valid {
		v := correlationID.String
		rec.CorrelationID = &v
	}
	return rec, nil
}

// ListEvents returns events with seq > since, ordered ascending, capped at
// limit entries (limit <= 0 defaults to [DefaultEventsPageSize]; any limit
// above [MaxEventsPageSize] is silently clamped down to it — see that
// constant's doc comment for why clamping rather than rejecting).
//
// The second return value, gap, reports whether one or more events with
// seq in the range (since, returned-or-current-oldest] have been deleted
// by [pruneEvents] and can never be returned by this or any future call.
// This is the API-visible half of the Step 3 task spec's requirement that
// pruning "must never delete an event that a caller is mid-page through in
// a way that produces a silent gap": ListEvents cannot un-delete anything,
// but it can — and does — say so, so a caller (Task D's API layer) can
// report the truth ("history before this point was not retained") instead
// of a response that looks identical to "nothing happened in that range."
//
// gap is always false when since == 0: a first-ever request with no prior
// cursor has nothing to be "torn" relative to, even if this coordinator's
// retention has already trimmed away everything before some later seq —
// that is ordinary bounded history, not a broken promise to a returning
// caller.
func (s *Store) ListEvents(ctx context.Context, since int64, limit int) (events []EventRecord, gap bool, err error) {
	if since < 0 {
		return nil, false, fmt.Errorf("store: list events: since must be >= 0, got %d", since)
	}
	switch {
	case limit <= 0:
		limit = DefaultEventsPageSize
	case limit > MaxEventsPageSize:
		limit = MaxEventsPageSize
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT`+eventColumns+`
		FROM events WHERE seq > ? ORDER BY seq ASC LIMIT ?
	`, since, limit)
	if err != nil {
		return nil, false, fmt.Errorf("store: list events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []EventRecord
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, false, fmt.Errorf("store: list events: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("store: list events: %w", err)
	}

	hasGap, err := s.eventsGapBefore(ctx, since)
	if err != nil {
		return nil, false, fmt.Errorf("store: list events: %w", err)
	}
	return out, hasGap, nil
}

// eventsGapBefore reports whether pruning has deleted any event with
// seq in (since, oldest currently stored], for since > 0. See
// [Store.ListEvents]'s doc comment for what this return value means to a
// caller and why since == 0 is unconditionally not a gap.
func (s *Store) eventsGapBefore(ctx context.Context, since int64) (bool, error) {
	if since == 0 {
		return false, nil
	}

	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(seq) FROM events`).Scan(&oldest); err != nil {
		return false, fmt.Errorf("check retention gap: %w", err)
	}
	if oldest.Valid {
		return since < oldest.Int64-1, nil
	}

	// The events table is currently empty — either nothing has ever been
	// appended, or every row that ever existed has since been pruned.
	// LatestEventSeq reads sqlite_sequence, which (per schemaV3's doc
	// comment in migrations.go) survives a full DELETE, so this still
	// tells the two cases apart: if since is behind that high-water mark,
	// something between since and latest existed and was pruned away.
	latest, err := s.LatestEventSeq(ctx)
	if err != nil {
		return false, err
	}
	return since < latest, nil
}

// LatestEventSeq returns the highest seq ever assigned by
// [Store.AppendEvent], or 0 if none has ever been appended. Per this
// method's role in the wire contract (a client fetches /api/v1/snapshot,
// reads its latestEventSeq, then polls /api/v1/events?since=that value
// "with no gap and no duplicate"), this reads sqlite_sequence rather than
// MAX(seq) FROM events: MAX(seq) would silently report 0 (or the wrong,
// lower value) once every event has been pruned away, understating the
// true high-water mark and making an already-seen event look new again to
// a client that naively resumed from 0. See schemaV3's doc comment in
// migrations.go for why AUTOINCREMENT is what makes sqlite_sequence durable
// against exactly that — regardless of how the events table came to be
// empty; [Store.pruneEvents] itself never removes the row an in-progress
// AppendEvent call just inserted (its recorded_at always equals the same
// "now" the prune pass's own cutoff is computed from, so that row is
// never older than the cutoff), so this package's own pruning alone can
// never empty the table, but LatestEventSeq does not rely on that as an
// invariant: see TestLatestEventSeqSurvivesEventsBeingFullyDeleted in
// events_test.go, which empties the table by a means other than
// AppendEvent's own pruning specifically to avoid assuming one code path's
// current behavior is the only way the table could ever become empty.
func (s *Store) LatestEventSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("store: latest event seq: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// OldestEventSeq returns the lowest seq currently retained by the events
// table, and true — or (0, false, nil) if the table currently retains no
// event at all (either none has ever been appended, or every row that ever
// existed has since been pruned).
//
// This method exists because internal/coordinator/api's EventReader
// interface (declared independently, per the Step 3 contract's "declare
// interfaces at the consumer" rule) needs it to report the events
// response's "oldestRetainedSeq" field on every response — not only when
// [Store.ListEvents]'s gap return value is true — and this package had no
// method that answered it until the Step 3 wiring pass added this one: a
// seam where the consumer-declared interface and the producer's actual
// method set had simply never been checked against each other before
// wiring time, exactly the shape of gap this step's contract warns about.
// Unlike [Store.LatestEventSeq] (which deliberately reads sqlite_sequence
// so the high-water mark survives every row being deleted), this reads
// MIN(seq) FROM events directly: there is no equivalent low-water-mark
// bookkeeping table for the oldest row, and there does not need to be — an
// oldest bound that becomes "none" once the table is empty is the correct
// answer for this field, unlike the latest-seq case where "forgetting" the
// high-water mark would let an old client's already-seen seq look new
// again.
func (s *Store) OldestEventSeq(ctx context.Context) (int64, bool, error) {
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(seq) FROM events`).Scan(&oldest); err != nil {
		return 0, false, fmt.Errorf("store: oldest event seq: %w", err)
	}
	if !oldest.Valid {
		return 0, false, nil
	}
	return oldest.Int64, true, nil
}
