package inventory

import (
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

var baseTime = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

func timePtr(t time.Time) *time.Time { return &t }

// TestDeriveLivenessMatrix drives deriveLiveness across the full matrix the
// Step 2 round 2 store task spec calls for. This is the retained-freshness
// rule's decisive test: a retained health delivery (ObservedAt == nil) must
// never produce LivenessOnline, and a live one within the staleness window
// must.
func TestDeriveLivenessMatrix(t *testing.T) {
	cases := []struct {
		name string
		rec  store.NodeRecord
		want Liveness
	}{
		{
			name: "no data at all",
			rec:  store.NodeRecord{NodeID: "n"},
			want: LivenessUnknown,
		},
		{
			name: "retained hello only",
			rec: store.NodeRecord{NodeID: "n", Hello: &store.HelloRecord{
				Label: "n", ObservedAt: nil, Provenance: store.ProvenanceRetainedBrokerState, Retained: true,
			}},
			want: LivenessUnknown,
		},
		{
			name: "last-will online, fresh live heartbeat",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: true, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-5 * time.Second)),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessOnline,
		},
		{
			name: "last-will online, stale live heartbeat",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: true, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-(StalenessWindow + time.Second))),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessUnknown,
		},
		{
			name: "last-will online, only retained health evidence (unknown age)",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: true, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: nil, Provenance: store.ProvenanceRetainedBrokerState, Retained: true,
				},
			},
			want: LivenessUnknown,
		},
		{
			name: "last-will online, no health evidence yet",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: true, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
			},
			want: LivenessUnknown,
		},
		{
			name: "last-will offline",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
			},
			want: LivenessOffline,
		},
		{
			// CLEAN SHUTDOWN: the agent's last deliberate act is to publish
			// its own retained "online: false" AFTER its last heartbeat.
			// The heartbeat is only a second old and well within the
			// staleness window, but it is OLDER than the offline last
			// will, so this is history, not a disagreement: offline must
			// win immediately. See deriveLiveness's "DISAGREEMENT IS ABOUT
			// ORDER, NOT FRESHNESS" doc section — this is the exact case an
			// earlier, freshness-only version of this rule got wrong.
			name: "clean shutdown: heartbeat before offline last will -> offline promptly",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-time.Second)),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessOffline,
		},
		{
			// UNCLEAN KILL: the broker's own Will fires after the node's
			// last heartbeat (the node never got a chance to publish
			// anything itself). Same shape as the clean-shutdown case from
			// deriveLiveness's point of view — the offline evidence is
			// newer than the heartbeat — so this must also be offline
			// promptly, not held at unknown for the staleness window.
			name: "unclean kill: broker will observed after last heartbeat -> offline promptly",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-3 * time.Second)),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessOffline,
		},
		{
			// GENUINE ZOMBIE: a live heartbeat is observed AFTER an offline
			// last will — the node kept proving it was alive after saying
			// (or having the broker say) it was going offline. This is the
			// one case where the evidence truly conflicts: unknown, not a
			// confident offline.
			name: "zombie: live heartbeat after offline last will disagrees -> unknown",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime.Add(-5 * time.Second)), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-time.Second)),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessUnknown,
		},
		{
			// RETAINED LAST WILL, FRESH LIVE HEARTBEAT: a just-restarted
			// coordinator replays a retained offline last will (age
			// unknown — ObservedAt nil) but a live heartbeat is already
			// arriving. The retained delivery's age can never be proven
			// newer than a live heartbeat happening right now, so this
			// counts as a disagreement even though there is no timestamp
			// to compare: the "node came back and has not yet republished
			// its online will" case.
			name: "retained offline last will with a fresh live heartbeat disagrees -> unknown",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: nil, Provenance: store.ProvenanceRetainedBrokerState, Retained: true},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-time.Second)),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessUnknown,
		},
		{
			// A RETAINED heartbeat must never override an offline last
			// will: its age is unknown (that is what a nil ObservedAt
			// means), so it is not "current evidence" the way a live
			// heartbeat is, and this must stay exactly the offline verdict
			// it would have been with no health evidence at all.
			name: "last-will offline with only a retained heartbeat stays offline",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: nil, Provenance: store.ProvenanceRetainedBrokerState, Retained: true,
				},
			},
			want: LivenessOffline,
		},
		{
			// A live heartbeat that has already gone stale is not current
			// evidence either, so it cannot create a disagreement even
			// though it is newer than the last will: this must also stay
			// offline, not unknown-via-disagreement.
			name: "last-will offline with a stale live heartbeat stays offline",
			rec: store.NodeRecord{
				NodeID: "n",
				LWT:    &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime.Add(-(StalenessWindow + 2*time.Second))), Provenance: store.ProvenanceAgentReport},
				Health: &store.HealthRecord{
					BootID: "b1", Sequence: 1,
					ObservedAt: timePtr(baseTime.Add(-(StalenessWindow + time.Second))),
					Provenance: store.ProvenanceAgentReport,
				},
			},
			want: LivenessOffline,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := deriveLiveness(tc.rec, baseTime)
			if got != tc.want {
				t.Errorf("deriveLiveness() = %q (%s), want %q", got, reason, tc.want)
			}
			// Online is the one verdict with no reason to give (see
			// TestDeriveLivenessOnlineHasNoReason); every other verdict must
			// explain itself.
			if reason == "" && tc.want != LivenessOnline {
				t.Errorf("reason is empty, want a non-empty explanation for a %q verdict", tc.want)
			}
		})
	}
}

func TestDeriveLivenessOnlineHasNoReason(t *testing.T) {
	rec := store.NodeRecord{
		NodeID: "n",
		LWT:    &store.LWTRecord{Online: true, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
		Health: &store.HealthRecord{
			BootID: "b1", Sequence: 1,
			ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport,
		},
	}
	liveness, reason := deriveLiveness(rec, baseTime)
	if liveness != LivenessOnline {
		t.Fatalf("Liveness = %q, want online", liveness)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty for a healthy online verdict", reason)
	}
}

// TestDeriveLivenessStalenessBoundary checks the exact edge: evidence
// exactly at StalenessWindow is still fresh (age > window is what tips to
// unknown, not age >= window).
func TestDeriveLivenessStalenessBoundary(t *testing.T) {
	rec := store.NodeRecord{
		NodeID: "n",
		LWT:    &store.LWTRecord{Online: true, ObservedAt: timePtr(baseTime), Provenance: store.ProvenanceAgentReport},
		Health: &store.HealthRecord{
			BootID: "b1", Sequence: 1,
			ObservedAt: timePtr(baseTime.Add(-StalenessWindow)),
			Provenance: store.ProvenanceAgentReport,
		},
	}
	liveness, _ := deriveLiveness(rec, baseTime)
	if liveness != LivenessOnline {
		t.Errorf("Liveness = %q at exactly the staleness window, want online", liveness)
	}
}

// TestDeriveLivenessDisagreementReasonNamesBothTopics checks the reason
// string itself, not just the verdict: per the coordinator's review
// feedback, a disagreement between last-will and a live heartbeat must
// read as a disagreement, not as a generic staleness message, so an
// operator (or a log line) can tell the two apart.
func TestDeriveLivenessDisagreementReasonNamesBothTopics(t *testing.T) {
	rec := store.NodeRecord{
		NodeID: "n",
		// The last will is OLDER than the heartbeat (observed 5s before
		// baseTime; the heartbeat is 2s before baseTime), so per the
		// ordering rule this is a genuine disagreement: the node kept
		// heartbeating after the last will.
		LWT: &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime.Add(-5 * time.Second)), Provenance: store.ProvenanceAgentReport},
		Health: &store.HealthRecord{
			BootID: "b1", Sequence: 7,
			ObservedAt: timePtr(baseTime.Add(-2 * time.Second)),
			Provenance: store.ProvenanceAgentReport,
		},
	}
	liveness, reason := deriveLiveness(rec, baseTime)
	if liveness != LivenessUnknown {
		t.Fatalf("Liveness = %q, want unknown", liveness)
	}
	if !strings.Contains(reason, "last-will") || !strings.Contains(reason, "disagree") {
		t.Errorf("reason = %q, want it to name the last-will/heartbeat disagreement plainly, not read as ordinary staleness", reason)
	}
}

// TestDeriveLivenessDisagreementBoundary checks the exact edge for the
// disagreement case: a live heartbeat exactly at StalenessWindow still
// counts as fresh enough to disagree (age > window is what stops counting
// it, matching TestDeriveLivenessStalenessBoundary's boundary for the
// online path) — provided it is also newer than the last will, per the
// ordering rule.
func TestDeriveLivenessDisagreementBoundary(t *testing.T) {
	rec := store.NodeRecord{
		NodeID: "n",
		// Older than the heartbeat, so ordering alone does not disqualify
		// the disagreement; only the staleness boundary is under test.
		LWT: &store.LWTRecord{Online: false, ObservedAt: timePtr(baseTime.Add(-StalenessWindow - time.Second)), Provenance: store.ProvenanceAgentReport},
		Health: &store.HealthRecord{
			BootID: "b1", Sequence: 1,
			ObservedAt: timePtr(baseTime.Add(-StalenessWindow)),
			Provenance: store.ProvenanceAgentReport,
		},
	}
	liveness, _ := deriveLiveness(rec, baseTime)
	if liveness != LivenessUnknown {
		t.Errorf("Liveness = %q at exactly the staleness window, want unknown (still counts as a disagreeing live heartbeat)", liveness)
	}
}
