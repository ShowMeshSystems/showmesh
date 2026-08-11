package store

import (
	"time"

	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// Provenance records where one stored observation's evidence came from,
// per ADR-011 (every observation carries provenance) and the Step 2 round
// 2 shared contract's three-way split between a live agent publish, a
// broker-delivered Last Will, and a retained-store replay.
type Provenance string

const (
	// ProvenanceAgentReport is a live (non-retained, MQTT RETAIN=0) publish
	// received while the sending node was actively connected: proof of
	// life at ObservedAt.
	ProvenanceAgentReport Provenance = "agent_report"

	// ProvenanceBrokerLastWill previously labeled every LWT topic delivery,
	// regardless of retained/live. It is no longer produced by this
	// codebase: on review, that uniform labeling was itself a bug (an
	// agent's own live "online: true" report was mislabeled as a broker
	// last will — see the Step 2 round 2 fix notes), and there is in any
	// case no wire-level way for a subscriber to distinguish "the broker
	// fired this client's registered Will" from "the client published this
	// itself" — both arrive as an ordinary PUBLISH on the same topic. The
	// LWT topic now goes through the same classify(retained) path as hello
	// and health (see internal/coordinator/inventory.Manager.classify),
	// producing [ProvenanceAgentReport] for a live delivery and
	// [ProvenanceRetainedBrokerState] for a retained replay, exactly like
	// every other topic. This constant is kept only so a database written
	// by an older binary, which may still contain the string
	// "broker_last_will" in node_lwt.provenance, remains interpretable;
	// nothing in this codebase writes it anymore.
	ProvenanceBrokerLastWill Provenance = "broker_last_will"

	// ProvenanceRetainedBrokerState is a retained-store replay (MQTT
	// RETAIN=1) of a hello or health message: the broker's record of the
	// last value published, with no information about how long ago that
	// was. Per the shared contract this must never be read as proof of
	// current life; see [HelloRecord.ObservedAt] and
	// [HealthRecord.ObservedAt].
	ProvenanceRetainedBrokerState Provenance = "retained_broker_state"
)

// HelloRecord is one node's stored capability advertisement, decoded from
// mqttproto.HelloPayload plus the evidence metadata the shared contract
// requires.
type HelloRecord struct {
	Label        string
	Platform     string
	AgentVersion string
	BootID       string
	StartedAt    time.Time
	Capabilities capability.Set

	// ObservedAt is nil when this hello's most recent delivery was a
	// retained replay (Provenance == ProvenanceRetainedBrokerState): per
	// the shared contract, a retained delivery's age is unknown, and
	// storing a receipt time here would silently smuggle a false freshness
	// claim into the model. Non-nil only for a live delivery (Provenance
	// == ProvenanceAgentReport).
	ObservedAt *time.Time
	Provenance Provenance
	Retained   bool
}

// LWTRecord is a node's last observed last-will/online-state evidence.
//
// ObservedAt and Provenance follow exactly the same retained-freshness rule
// as [HelloRecord] and [HealthRecord] (see [HelloRecord.ObservedAt]'s doc
// comment): nil/[ProvenanceRetainedBrokerState] for a retained delivery
// (unknown age — this is a coordinator restart replaying whatever the
// broker last held, possibly hours old), non-nil/[ProvenanceAgentReport]
// for a live one. This LWTRecord previously special-cased the LWT topic to
// always stamp the coordinator's own receipt time and a constant
// [ProvenanceBrokerLastWill] provenance regardless of Retained, on the
// reasoning that an offline declaration on this topic is trustworthy
// "in both directions" so there was supposedly no freshness claim to
// protect. That reasoning does not survive contact with what the field
// actually gets read for: deriveLiveness never reads LWT.ObservedAt (the
// offline branch is unconditional on freshness, by design — see
// liveness.go), but [internal/coordinator/inventory.NodeView] and any
// future consumer of it (e.g. Step 3's read API) can, and a coordinator
// restarting at 03:00 that replays a retained "online: true" published at
// 21:00 the previous evening must not be able to stamp that as "observed at
// 03:00" — that is precisely the false-freshness failure ADR-011 exists to
// prevent, just for the LWT topic instead of health. See
// internal/coordinator/inventory.Manager.classify, the one place this rule
// is actually implemented, for both this type and [HelloRecord]/
// [HealthRecord].
type LWTRecord struct {
	Online     bool
	Reason     string
	ObservedAt *time.Time
	Provenance Provenance
	Retained   bool
}

// HealthRecord is one node's most recently accepted health heartbeat.
// "Accepted" already excludes a duplicate or reordered delivery within the
// same boot session (see [Store.RecordHealth]); only the highest-sequence
// record for the current boot ID is ever the one stored.
type HealthRecord struct {
	BootID     string
	Sequence   uint64
	AgentState string
	UptimeMS   int64

	// ObservedAt is nil for a retained delivery; see
	// [HelloRecord.ObservedAt]'s doc comment, which applies identically
	// here.
	ObservedAt *time.Time
	Provenance Provenance
	Retained   bool
}

// NodeRecord is everything the store knows about one node: its identity
// and hello contents (if any hello has ever been observed), its last-will
// evidence (if any), and its most recently accepted health heartbeat (if
// any). A nil field means "never observed" — it is never used to mean
// "observed as zero/empty"; a node can legitimately exist in inventory
// (e.g. from a health or LWT message that arrived before its hello did)
// with Hello == nil.
//
// NodeRecord deliberately carries no derived liveness verdict. See the
// package doc comment for why that is computed by
// internal/coordinator/inventory on read, plus the caller's current time,
// rather than stored here.
type NodeRecord struct {
	NodeID string

	Hello  *HelloRecord
	LWT    *LWTRecord
	Health *HealthRecord

	// FirstSeenAt and UpdatedAt are store bookkeeping timestamps (the
	// store's own clock, not observation evidence): when a row for this
	// node first existed, and when it was last written to, for any reason.
	FirstSeenAt time.Time
	UpdatedAt   time.Time
}
