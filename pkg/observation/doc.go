// Package observation is the canonical evidence model from OBSERVABILITY
// section 4.1 (observations) and 4.2 (resource health): the shared
// vocabulary every collector produces, the coordinator's store persists,
// and the API renders. It lives in pkg/ rather than internal/, alongside
// pkg/mqttproto and pkg/capability, because node agents will eventually
// produce observations too (OBSERVABILITY section 2.6: collectors and node
// agents timestamp observations close to their sources) — this is shared
// vocabulary, not a coordinator internal.
//
// This is a pure domain package: no I/O, no SQL, no HTTP, no MQTT. It
// exists so that ADR-011 ("stale or insufficient evidence becomes unknown,
// not healthy") is structural — enforced by the type and its constructors —
// rather than a rule every collector and every render path has to remember
// on its own. The wire types that put this on the network live in
// internal/coordinator/api/v1 and are mapped from these, deliberately, so
// that a change to this domain model cannot silently retype the public
// contract; JSON tags and marshaling belong there, never here.
//
// # Why ObservedAt is a pointer, and what nil means
//
// [Observation.ObservedAt] answers "when was the measured condition true?",
// which is a different question from [Observation.CollectedAt] ("when did
// the collector record this?"). The two coincide for most direct polling,
// but they diverge in exactly the case this project has already been
// burned by: a retained MQTT delivery. The broker replays a retained
// message on subscribe, so CollectedAt is "just now" no matter how old the
// evidence actually is. If ObservedAt defaulted to CollectedAt, an
// hours-old heartbeat from a node that has since lost power would read as
// perfectly fresh — silently, because the field would always be set and
// nothing downstream would know to doubt it.
//
// A nil ObservedAt means exactly one thing: the observation time is
// genuinely unknown, not merely inconvenient to compute. [MeasuredUnknownAge]
// is the only constructor that produces one, and [Observation.StateAt]
// derives [StateUnknownAge] for it — a state that, like every non-current
// state, can never be reported as [HealthHealthy] (see [DeriveHealth]).
//
// Do not "fix" a nil ObservedAt by filling it in from CollectedAt, a zero
// time, or time.Now(). That is not a bug fix; it is reintroducing the exact
// defect ADR-011 exists to prevent, and it will not show up as a compile
// error or even a test failure in the collector that does it — only later,
// as an operator being told a dead node is fine.
//
// # Why the absence vocabulary has exactly these members
//
// [State] has six members because BUILD-PLAN Step 3 requires distinguishing
// three failure-to-observe cases the operator surface must not collapse
// into one "no data" blob — [StateUnsupported] (this source cannot ever
// provide this signal), [StateNotCollected] (no attempt has been made yet:
// not configured, disabled, or no poll has completed), and
// [StateCollectionFailed] (an attempt was made and failed) — plus the two
// freshness cases ADR-011 requires once a value does exist —
// [StateUnknownAge] (see above) and [StateStale] (aged past
// [Observation.ValidFor]) — and [StateCurrent] for the case where none of
// the above apply. Nothing here is derived from a sixth case; a helper that
// needs a different answer belongs above this package, not as an addition
// to this list.
package observation
