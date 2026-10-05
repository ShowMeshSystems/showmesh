// Package nodefallback turns a node's showmesh.node.fallback/v1 report
// into observations, so every answer a node gave on its fallback routes
// (ADR-048 decision 3) is visible to an operator.
package nodefallback

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SourceName is this collector's id and the prefix of every observation's source.
const SourceName = "node-fallback"

// Every value is a scalar, which is all an observation can carry. The
// node's retained report keeps the full list of recent answers.
const (
	SignalCoordinatorKeyLoaded observation.SignalID = "node.fallback.coordinator_key_loaded"
	SignalProgramCount         observation.SignalID = "node.fallback.program_count"
	// SignalEnrolledProgramCount counts held programs that carry an
	// executor key. SignalUnenrolledFPP lists the FPP instance UUIDs of
	// the ones that do not, comma separated, empty when there are none.
	SignalEnrolledProgramCount observation.SignalID = "node.fallback.enrolled_program_count"
	SignalUnenrolledFPP        observation.SignalID = "node.fallback.unenrolled_fpp_instance_uuids"
	// SignalExecutionRecordProblem is the node's own sentence for why it
	// cannot record handled requests, empty when it can.
	SignalExecutionRecordProblem observation.SignalID = "node.fallback.execution_record_problem"
	SignalAcceptedCount          observation.SignalID = "node.fallback.accepted_count"
	SignalRefusedCount           observation.SignalID = "node.fallback.refused_count"
	SignalLastOutcome            observation.SignalID = "node.fallback.last_outcome"
	SignalLastReason             observation.SignalID = "node.fallback.last_reason"
	SignalLastAt                 observation.SignalID = "node.fallback.last_at"
	SignalLastRefusalOutcome     observation.SignalID = "node.fallback.last_refusal_outcome"
	SignalLastRefusalReason      observation.SignalID = "node.fallback.last_refusal_reason"
	SignalLastRefusalAt          observation.SignalID = "node.fallback.last_refusal_at"
	SignalLastRefusalFPP         observation.SignalID = "node.fallback.last_refusal_fpp_instance_uuid"
)

// AllSignalIDs is every signal this collector emits for a node.
var AllSignalIDs = []observation.SignalID{
	SignalCoordinatorKeyLoaded, SignalProgramCount, SignalEnrolledProgramCount, SignalUnenrolledFPP,
	SignalExecutionRecordProblem, SignalAcceptedCount, SignalRefusedCount,
	SignalLastOutcome, SignalLastReason, SignalLastAt,
	SignalLastRefusalOutcome, SignalLastRefusalReason, SignalLastRefusalAt, SignalLastRefusalFPP,
}

// DefaultPollInterval is how often the collector re-reads the store.
const DefaultPollInterval = 5 * time.Second

// DefaultValidFor covers two of the node's one-minute report ticks.
const DefaultValidFor = 150 * time.Second

type report struct {
	payload    mqttproto.FallbackPayload
	receivedAt time.Time
}

// Store holds each node's latest fallback report.
type Store struct {
	mu   sync.Mutex
	data map[string]report
}

func NewStore() *Store {
	return &Store{data: make(map[string]report)}
}

// Put records payload as nodeID's latest report.
func (s *Store) Put(nodeID string, payload mqttproto.FallbackPayload, receivedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[nodeID] = report{payload: payload, receivedAt: receivedAt}
}

func (s *Store) snapshot() map[string]report {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]report, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

var _ collector.Collector = (*Collector)(nil)

// Collector emits the node.fallback.* signals for every node in its store.
type Collector struct {
	store *Store
}

func New(store *Store) *Collector {
	return &Collector{store: store}
}

func (c *Collector) ID() string { return SourceName }

func (c *Collector) Poll(context.Context) ([]observation.Observation, bool) {
	var obs []observation.Observation
	for nodeID, rep := range c.store.snapshot() {
		obs = append(obs, nodeObservations(nodeID, rep)...)
	}
	return obs, true
}

func nodeObservations(nodeID string, rep report) []observation.Observation {
	p := rep.payload
	measured := func(sig observation.SignalID, value any) observation.Observation {
		return buildValue(nodeID, sig, value, p.ObservedAt, rep)
	}
	var enrolled int64
	var unenrolled []string
	for _, program := range p.Programs {
		if program.ExecutorEnrolled {
			enrolled++
		} else {
			unenrolled = append(unenrolled, program.FPPInstanceUUID)
		}
	}
	sort.Strings(unenrolled)
	obs := []observation.Observation{
		measured(SignalCoordinatorKeyLoaded, p.CoordinatorKeyLoaded),
		measured(SignalProgramCount, int64(len(p.Programs))),
		measured(SignalEnrolledProgramCount, enrolled),
		measured(SignalUnenrolledFPP, strings.Join(unenrolled, ",")),
		measured(SignalExecutionRecordProblem, p.ExecutionRecordProblem),
		measured(SignalAcceptedCount, p.Accepted),
		measured(SignalRefusedCount, p.Refused),
	}

	var last, lastRefusal *mqttproto.FallbackDecision
	for i := range p.Decisions {
		last = &p.Decisions[i]
		if !p.Decisions[i].Accepted {
			lastRefusal = &p.Decisions[i]
		}
	}
	if last != nil {
		obs = append(obs,
			measured(SignalLastOutcome, last.Outcome),
			measured(SignalLastReason, last.Reason),
			measured(SignalLastAt, last.At.UTC().Format(time.RFC3339Nano)))
	} else {
		const reason = "this node has answered no fallback request since it started"
		for _, sig := range []observation.SignalID{SignalLastOutcome, SignalLastReason, SignalLastAt} {
			obs = append(obs, notCollected(nodeID, sig, reason, rep.receivedAt))
		}
	}
	if lastRefusal != nil {
		obs = append(obs,
			measured(SignalLastRefusalOutcome, lastRefusal.Outcome),
			measured(SignalLastRefusalReason, lastRefusal.Reason),
			measured(SignalLastRefusalAt, lastRefusal.At.UTC().Format(time.RFC3339Nano)),
			measured(SignalLastRefusalFPP, lastRefusal.FPPInstanceUUID))
	} else {
		const reason = "this node has refused no fallback request among its recent answers"
		for _, sig := range []observation.SignalID{SignalLastRefusalOutcome, SignalLastRefusalReason, SignalLastRefusalAt, SignalLastRefusalFPP} {
			obs = append(obs, notCollected(nodeID, sig, reason, rep.receivedAt))
		}
	}
	return obs
}

func sourceFor(nodeID string) string { return SourceName + ":" + nodeID }

func notCollected(nodeID string, sig observation.SignalID, reason string, at time.Time) observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	o, err := observation.NotCollected(res, sig, reason, observation.WithSource(sourceFor(nodeID)), observation.WithCollectedAt(at))
	if err != nil {
		panic(fmt.Sprintf("nodefallback: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func buildValue(nodeID string, sig observation.SignalID, value any, observedAt *time.Time, rep report) observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	opts := []observation.Option{observation.WithSource(sourceFor(nodeID)), observation.WithCollectedAt(rep.receivedAt)}
	if observedAt != nil {
		if o, err := observation.Measured(res, sig, value, *observedAt, append(opts, observation.WithValidFor(DefaultValidFor))...); err == nil {
			return o
		}
	}
	o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
	if err != nil {
		panic(fmt.Sprintf("nodefallback: MeasuredUnknownAge(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}
