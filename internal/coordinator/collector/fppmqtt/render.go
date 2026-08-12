package fppmqtt

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift between the two packages is caught here.
var _ collector.Collector = (*Collector)(nil)

// ID returns "fpp-mqtt" — this Collector's identity in the API's
// collectors[] list and every observation's Source field.
func (c *Collector) ID() string { return sourceName }

// Poll renders whatever this collector's subscription callback has
// received so far (see store.go) into observations. It never blocks on the
// network: the MQTT connection is owned and driven entirely by Run,
// independent of when or how often Poll is called (contract section 4.1's
// push-to-poll shape). ctx is accepted to satisfy collector.Collector but
// is not used for any I/O here — there is none to bound.
//
// Poll always returns complete=true, including before this collector's
// first message ever arrives and while the broker connection is down. This
// is not an optimistic default; it is what this method's own shape
// guarantees: every call iterates every configured host (c.hosts, fixed at
// construction — see New) and, for a connected host, every statically-known
// topicSpec (topics.go), producing SOME observation for every signal this
// collector is capable of ever reporting — a measured value, a retained-
// unknown-age value, or an absence (StateNotCollected when a topic's
// message has simply never arrived yet, StateCollectionFailed when the
// broker connection itself is down or a message failed to decode). There is
// no partial-render path here the way fpp.Collector's backoff-skip is: this
// package has nothing to back off from (Poll is a local, non-blocking
// render of a cache; see the doc comment above), so it never has a reason
// to omit a signal it knows how to report on. A signal this collector has
// no static knowledge of at all (it declares no dynamic per-element signal
// family the way fpp.Collector's ports/sensors do — see topics.go) is
// simply never in its vocabulary to begin with, which is a different
// question from completeness and is unaffected by this claim.
func (c *Collector) Poll(_ context.Context) ([]observation.Observation, bool) {
	now := c.now()
	connected, connReason := c.connectionState()

	obs := make([]observation.Observation, 0, len(c.hosts)*len(allStaticSignalIDs))
	for instanceID := range c.hosts {
		if !connected {
			obs = append(obs, c.connectionDownObservations(instanceID, connReason, now)...)
			continue
		}
		obs = append(obs, c.instanceObservations(instanceID, now)...)
	}
	return obs, true
}

// connectionDownObservations builds a StateCollectionFailed observation,
// all naming reason, for every statically-known signal this collector
// models for instanceID. Contract section 4.1: "The connection state
// itself is a signal — a collection_failed on every signal with the
// reason naming the broker failure when the connection is down, never
// silence." This deliberately OVERRIDES whatever is cached in the message
// store: if the broker connection itself is down, the coordinator states
// that plainly rather than continuing to serve a topic's last-known value
// as though this source were still vouching for it.
func (c *Collector) connectionDownObservations(instanceID, reason string, now time.Time) []observation.Observation {
	resource := observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID}
	obs := make([]observation.Observation, 0, len(allStaticSignalIDs))
	for _, sig := range allStaticSignalIDs {
		obs = append(obs, c.failed(resource, sig, reason, now))
	}
	return obs
}

// instanceObservations renders the current message-store snapshot for one
// configured instance into observations, one topicSpec at a time.
func (c *Collector) instanceObservations(instanceID string, now time.Time) []observation.Observation {
	resource := observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID}
	snap := c.store.snapshot(instanceID)

	var obs []observation.Observation
	for suffix, spec := range topicSpecs {
		msg, ok := snap[suffix]
		if !ok {
			reason := fmt.Sprintf("no message received on topic %q since this collector started", suffix)
			for _, sig := range spec.staticSignals {
				obs = append(obs, c.notCollected(resource, sig, reason, now))
			}
			continue
		}

		values, err := spec.decode(msg.payload)
		if err != nil {
			reason := "decode error: " + err.Error()
			for _, sig := range spec.staticSignals {
				obs = append(obs, c.failedAt(resource, sig, reason, msg.receivedAt))
			}
			continue
		}

		for _, sv := range values {
			obs = append(obs, c.buildObservation(resource, sv, msg))
		}
	}
	return obs
}

// buildObservation is where contract section 4.2's retained/live
// distinction is enforced, for every value-bearing signalValue this
// package produces. It is the one place in this package that decides
// ObservedAt, and it is deliberately small and un-clever:
//
//   - sv carries an Absence (Unsupported/CollectionFailed/NotCollected):
//     dispatch to the matching observation.* constructor. Absence
//     observations carry no ObservedAt in this model at all (see
//     pkg/observation), so retained/live is not applicable here — the
//     distinction only matters for a value.
//   - msg.retained: [observation.MeasuredUnknownAge]. ObservedAt is nil.
//     NEVER msg.receivedAt — that is precisely the defect contract section
//     4.2 names as having been "introduced and caught three times in this
//     project in different disguises."
//   - otherwise (a live delivery): [observation.Measured] with
//     msg.receivedAt as ObservedAt — the moment this collector actually
//     saw the message, which is the earliest defensible "this was true"
//     evidence a push source can offer, exactly as
//     internal/coordinator/broker's Message.Retained doc comment argues
//     for the control-plane case this package deliberately does not share
//     code with (see doc.go).
//
// CollectedAt is msg.receivedAt in BOTH branches, not the later moment
// Poll happens to run: msg.receivedAt is when this collector actually
// recorded the evidence (the push callback fired), which is what
// CollectedAt means per pkg/observation's doc comment ("bookkeeping, not
// evidence of the subject's state") — Poll merely renders a cache, it does
// not itself collect anything.
func (c *Collector) buildObservation(resource observation.ResourceRef, sv fpp.SignalValue, msg message) observation.Observation {
	opts := []observation.Option{
		observation.WithSource(sourceName),
		observation.WithCollectedAt(msg.receivedAt),
	}
	if sv.Unit != "" {
		opts = append(opts, observation.WithUnit(sv.Unit))
	}

	if sv.Absence != "" {
		return c.absenceObservation(resource, sv.Signal, sv.Absence, sv.Reason, opts, msg.receivedAt)
	}

	if msg.retained {
		o, err := observation.MeasuredUnknownAge(resource, sv.Signal, sv.Value, opts...)
		if err != nil {
			return c.failed(resource, sv.Signal, internalErrorReason(err), msg.receivedAt)
		}
		return o
	}

	opts = append(opts, observation.WithValidFor(c.validFor))
	o, err := observation.Measured(resource, sv.Signal, sv.Value, msg.receivedAt, opts...)
	if err != nil {
		return c.failed(resource, sv.Signal, internalErrorReason(err), msg.receivedAt)
	}
	return o
}

func (c *Collector) absenceObservation(resource observation.ResourceRef, sig observation.SignalID, state observation.State, reason string, opts []observation.Option, now time.Time) observation.Observation {
	var (
		o   observation.Observation
		err error
	)
	switch state {
	case observation.StateUnsupported:
		o, err = observation.Unsupported(resource, sig, reason, opts...)
	case observation.StateNotCollected:
		o, err = observation.NotCollected(resource, sig, reason, opts...)
	case observation.StateCollectionFailed:
		o, err = observation.CollectionFailed(resource, sig, reason, opts...)
	default:
		// A decode.go bug (an Absence state outside the three this package
		// ever constructs), not a runtime condition — surfaced as
		// collection_failed rather than panicking, so it is visible on the
		// API instead of invisible.
		return c.failed(resource, sig, fmt.Sprintf("internal error: unexpected absence state %q", state), now)
	}
	if err != nil {
		return c.failed(resource, sig, internalErrorReason(err), now)
	}
	return o
}

// failed/failedAt/notCollected build the three "no value" observation
// kinds via pkg/observation's constructors, each stamped with this
// Collector's Source. failed uses now for both CollectedAt and (via the
// constructor) as the moment of the failure verdict; failedAt lets a
// caller stamp a specific receipt time instead (used when a topic's
// message WAS received but failed to decode, so CollectedAt should be the
// receipt time, not the later Poll time — see instanceObservations).
func (c *Collector) failed(resource observation.ResourceRef, sig observation.SignalID, reason string, now time.Time) observation.Observation {
	return c.failedAt(resource, sig, reason, now)
}

func (c *Collector) failedAt(resource observation.ResourceRef, sig observation.SignalID, reason string, at time.Time) observation.Observation {
	o, err := observation.CollectionFailed(resource, sig, reason,
		observation.WithSource(sourceName), observation.WithCollectedAt(at))
	if err != nil {
		// reason is always non-empty and resource/sig are always set by
		// every call site in this package; a failure here is a bug in
		// this file, not a runtime condition to degrade from gracefully.
		panic(fmt.Sprintf("fppmqtt: CollectionFailed(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func (c *Collector) notCollected(resource observation.ResourceRef, sig observation.SignalID, reason string, now time.Time) observation.Observation {
	o, err := observation.NotCollected(resource, sig, reason,
		observation.WithSource(sourceName), observation.WithCollectedAt(now))
	if err != nil {
		panic(fmt.Sprintf("fppmqtt: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func internalErrorReason(err error) string {
	return fmt.Sprintf("internal error building observation: %v", err)
}
