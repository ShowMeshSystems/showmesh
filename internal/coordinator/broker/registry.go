// This file adds a named-broker registry on top of BrokerManager: a lookup
// from a broker identifier — the value an mqtt show.action target names in
// its "broker" field — to the single BrokerManager connected to it.
//
// It exists because the coordinator can be configured with more than one
// MQTT broker: its own control-plane broker (SHOWMESH_MQTT_BROKER, what
// [BrokerManager] built by the coordinator's own startup path connects to)
// and, separately, any number of integration-target brokers such as the
// operator's home-automation broker (SHOWMESH_FPP_MQTT_BROKER_URL). An
// action names which one it means by identifier, with no default: a
// defaulted broker would silently point a step at the wrong integration
// target, indistinguishable from the target itself misbehaving.
//
// Registry itself dials nothing and owns no configuration. Deciding which
// identifiers exist, building a BrokerManager for each, and registering them
// here is wave 2's job, in the config and coordinator wiring layers this
// package does not touch — see this package's task specification. Registry
// only guarantees that once a caller resolves an identifier, every
// subsequent operation for that call happens against that one BrokerManager.
package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrUnknownBroker is returned by Registry.Get, and by anything built on
// it, when the caller names a broker identifier the registry has nothing
// registered for. An empty identifier is unknown too, deliberately: there
// is no default broker, so a caller that forgot to name one gets the same
// refusal as a caller that named a broker that does not exist, rather than
// silently resolving to whichever broker happens to be registered.
var ErrUnknownBroker = errors.New("broker: unknown broker identifier")

// ErrBrokerAlreadyRegistered is returned by Registry.Register when id is
// already registered. Re-registering under the same identifier is refused
// rather than silently replacing the existing BrokerManager: a waiter
// already in flight against the old BrokerManager (see
// BrokerManager.AwaitResponse) would otherwise keep running against a
// connection the registry no longer considers the named broker, with
// nothing anywhere reporting that the identifier now means something else.
var ErrBrokerAlreadyRegistered = errors.New("broker: identifier already registered")

// Registry resolves a broker identifier to the [BrokerManager] connected to
// it. It is safe for concurrent use.
//
// Registry.Publish and Registry.AwaitResponse are the intended call sites
// for the macro executor (wave 2): both resolve id exactly once and then
// perform the entire operation against that single BrokerManager, so a
// publish and the matching subscribe it waits on can never land on two
// different broker connections by construction. A caller that resolved the
// broker itself via Get and then called BrokerManager.Publish and a
// separately-resolved BrokerManager.AwaitResponse (or the reverse) could
// still make that mistake; Registry.AwaitResponse exists specifically so
// wave 2 has no need to do that.
type Registry struct {
	mu       sync.RWMutex
	managers map[string]*BrokerManager
}

// NewRegistry returns an empty Registry. Brokers are added with Register.
func NewRegistry() *Registry {
	return &Registry{managers: make(map[string]*BrokerManager)}
}

// Register adds bm under id. id must be non-empty, per this type's "no
// default broker" rule, and must not already be registered — see
// [ErrBrokerAlreadyRegistered].
func (r *Registry) Register(id string, bm *BrokerManager) error {
	if id == "" {
		return fmt.Errorf("%w: broker identifier must not be empty", ErrUnknownBroker)
	}
	if bm == nil {
		return errors.New("broker: cannot register a nil BrokerManager")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.managers[id]; exists {
		return fmt.Errorf("%w: %q", ErrBrokerAlreadyRegistered, id)
	}
	r.managers[id] = bm
	return nil
}

// Get resolves id to its registered BrokerManager. There is no default
// broker: an empty id, or an id nothing has registered, is
// [ErrUnknownBroker].
func (r *Registry) Get(id string) (*BrokerManager, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: broker identifier must not be empty", ErrUnknownBroker)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	bm, ok := r.managers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBroker, id)
	}
	return bm, nil
}

// IDs returns the currently registered broker identifiers, in no particular
// order. Intended for callers that need to validate an action's declared
// broker against the deployment's declared set (per this project's write-
// time referential validation rule) without resolving one in particular.
// The returned slice is a fresh copy on every call.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.managers))
	for id := range r.managers {
		ids = append(ids, id)
	}
	return ids
}

// Publish resolves id and publishes through that BrokerManager only. See
// [BrokerManager.Publish] for the underlying semantics.
func (r *Registry) Publish(ctx context.Context, id, topic string, qos byte, retain bool, payload []byte) error {
	bm, err := r.Get(id)
	if err != nil {
		return err
	}
	return bm.Publish(ctx, topic, qos, retain, payload)
}

// AwaitResponse resolves id exactly once and runs the whole
// publish-then-wait sequence (see [BrokerManager.AwaitResponse]) against
// that single BrokerManager, so the publish and the subscribe it waits on
// are structurally guaranteed to be the same broker connection — see
// [Registry]'s doc comment.
func (r *Registry) AwaitResponse(ctx context.Context, id string, req ResponseRequest) (Message, error) {
	bm, err := r.Get(id)
	if err != nil {
		return Message{}, err
	}
	return bm.AwaitResponse(ctx, req)
}
