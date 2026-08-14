package mqttproto

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Topic prefixes and literal segments of the ADR-008 v1 topic set. This is
// the exact and only topic set this package builds or parses; see the
// package doc comment for the scope boundary on what travels on cmd/result.
const (
	nodesPrefix  = "showmesh/nodes"
	eventsPrefix = "showmesh/events"

	segHello    = "hello"
	segObserved = "observed"
	segCmd      = "cmd"
	segResult   = "result"
	segLWT      = "lwt"
)

// DeliveryPolicy is the MQTT retain flag and QoS level a publisher should
// use for one kind of ShowMesh topic. This package has no MQTT client (see
// the package doc comment): DeliveryPolicy is data only, never behavior, so
// the agent and the coordinator — written by separate authors — read the
// same values here instead of each hardcoding ADR-008's retain/QoS
// conventions independently and drifting apart.
type DeliveryPolicy struct {
	Retain bool
	QoS    byte
}

// HelloDeliveryPolicy, ObservedDeliveryPolicy, CmdDeliveryPolicy, and
// ResultDeliveryPolicy are taken directly from ADR-008's decision text:
// "retained QoS 1 for state" for hello and observed, "QoS 1 for commands
// and results" for cmd and result — never retained, because a command or a
// result is a point-in-time message, not durable state a late-joining
// subscriber should replay. An agent that publishes hello non-retained
// fails in a way that looks like a coordinator bug (a late-joining
// coordinator simply sees nothing), which is exactly what these constants
// exist to prevent.
var (
	HelloDeliveryPolicy    = DeliveryPolicy{Retain: true, QoS: 1}
	ObservedDeliveryPolicy = DeliveryPolicy{Retain: true, QoS: 1}
	CmdDeliveryPolicy      = DeliveryPolicy{Retain: false, QoS: 1}
	ResultDeliveryPolicy   = DeliveryPolicy{Retain: false, QoS: 1}
)

// LWTDeliveryPolicy and EventDeliveryPolicy: SHOWMESH CHOICE, NOT AN
// ADR-008 REQUIREMENT. ADR-008 fixes that a Last Will exists and that
// events are coordinator-published, but its retain/QoS sentence does not
// name either topic.
//
// The LWT topic is retained because in ShowMesh it is a node's presence
// state, not a one-shot notification. The coordinator derives liveness by
// reading it on subscribe, so it has to survive for a late joiner: a
// coordinator that starts after a node has already died must still learn
// that the node is offline, and a coordinator that restarts while a node
// is healthy must still learn that it is online. Non-retained, both cases
// present as "no information", and the coordinator would report a dead
// node and a never-seen node identically.
//
// The Will's retain flag is registered by the client at CONNECT time, so
// it is the broker that applies it when publishing the Will on an
// abnormal disconnect. An agent must use the same retain setting for the
// explicit online/offline messages it publishes to this topic itself, so
// the retained value is consistent no matter which path last wrote it.
//
// This was originally set to Retain: false on the reasoning that a Will is
// delivered at most once per disconnect and leaves nothing durable to
// replay. That is true of a Will in the abstract and wrong for this
// design, where the topic is the liveness evidence rather than an alert.
//
// Events are QoS 1 and not retained: a lifecycle or alert event is a
// point-in-time notification, not state. Revisit either if ADR-008 is
// amended to say otherwise.
var (
	LWTDeliveryPolicy   = DeliveryPolicy{Retain: true, QoS: 1}
	EventDeliveryPolicy = DeliveryPolicy{Retain: false, QoS: 1}
)

// Subscription filters the coordinator needs to receive every node's hello,
// LWT, and observed-state topics. ADR-008 fixes the topics themselves
// (showmesh/nodes/<node-id>/hello and friends); the '+'/'#' wildcard filter
// strings that subscribe to all nodes at once are this package's own
// derivation from that topic shape, the way [nodeIDPattern] and
// [subpathSegmentPattern] below are ShowMesh choices, not ADR-008 text.
//
// SubscribeObserved's trailing "/#" also matches the bare parent topic
// showmesh/nodes/<node-id>/observed (MQTT's '#' matches zero or more
// levels, including none). No publisher in this package ever builds that
// bare topic — [ObservedTopic] requires a non-empty subpath — but nothing
// stops a misbehaving client from publishing to it directly, and
// [ParseTopic] rejects it as an invalid topic (observed topic requires a
// subpath) rather than treating it as a shorter valid observed topic. A
// subscriber must expect and handle that ParseTopic error on this filter,
// not log it as corruption.
const (
	SubscribeHello    = nodesPrefix + "/+/" + segHello
	SubscribeLWT      = nodesPrefix + "/+/" + segLWT
	SubscribeObserved = nodesPrefix + "/+/" + segObserved + "/#"
)

// nodeIDPattern is the node ID syntax: 1 to 64 characters of lowercase
// letters, digits, and internal hyphens, never starting or ending with a
// hyphen. This is security-relevant, not cosmetic: an unvalidated node ID
// is a topic-injection vector (a node calling itself "a/+/b" would
// subscribe or publish outside its own subtree), so the character class is
// deliberately narrow rather than merely "not empty". The {0,62} bound on
// the interior group caps total length at 1 + 62 + 1 = 64 without a
// separate length check.
//
// SHOWMESH HYPOTHESIS, NOT AN ADR-008 REQUIREMENT: ADR-008 fixes the topic
// shape (showmesh/nodes/<node-id>/...) but says nothing about what
// characters a node ID may contain or how long it may be; that is this
// package's own conservative choice, matching how
// pkg/multisync/timeline.go labels a threshold as a ShowMesh guess rather
// than an FPP-derived value. It permanently constrains what an operator may
// name a node (no underscore, no dot, no uppercase), so widen it here,
// deliberately, rather than reading it as a decision ADR-008 already made.
var nodeIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ErrInvalidNodeID is wrapped by every error [ValidateNodeID] returns, and
// by every topic builder that rejects a node ID.
var ErrInvalidNodeID = errors.New("mqttproto: invalid node ID")

// ValidateNodeID reports whether id is a syntactically valid node ID:
// 1-64 characters, [a-z0-9] internally with optional interior hyphens,
// never starting or ending with a hyphen. It rejects '/', '+', '#', '$',
// whitespace, non-ASCII, and the empty string, all in one pattern, because
// none of those characters are in the allowed class.
func ValidateNodeID(id string) error {
	if !nodeIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q: must be 1-64 characters of [a-z0-9-], not starting or ending with '-'",
			ErrInvalidNodeID, id)
	}
	return nil
}

// subpathSegmentPattern bounds each '/'-separated segment of an
// observed/<subpath> or events/<subpath> topic suffix, and a result/<cmd-id>
// segment: lowercase letters, digits, hyphen, and underscore, starting with
// a letter or digit. Like nodeIDPattern, this character class is what
// excludes '+', '#', empty segments (which would appear as "//"), and
// leading/trailing slashes, without a separate check for each.
//
// SHOWMESH HYPOTHESIS, NOT AN ADR-008 REQUIREMENT: ADR-008 fixes the topic
// shape (observed/<subpath>, result/<cmd-id>, and so on) but says nothing
// about what characters a subpath segment or a cmd-id may contain beyond
// what MQTT itself forbids. This specific character class is this
// package's own conservative choice, the way pkg/multisync/timeline.go
// labels a threshold as a ShowMesh guess rather than an FPP-derived value;
// it must not be read as something ADR-008 specifies. Widen it if a real
// subpath or cmd-id (e.g. a UUID, which is already covered, or something
// with different punctuation) needs characters outside it.
var subpathSegmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// maxSubpathLength bounds the total length of an observed/<subpath> or
// events/<subpath> suffix (including any internal '/' separators).
//
// SHOWMESH HYPOTHESIS, NOT AN ADR-008 REQUIREMENT: like
// [subpathSegmentPattern]'s character class, ADR-008 says nothing about how
// long a subpath may be. Without this bound, [ObservedTopic] and
// [EventTopic] would happily build (and [ParseTopic] would happily accept)
// a topic of arbitrary length, while [nodeIDPattern] caps the node ID
// segment of the same topic at 64. 256 is a conservative, unmeasured guess
// at "long enough for any real subpath, short enough to reject abuse";
// widen it if a real subpath needs more.
const maxSubpathLength = 256

// ErrInvalidSubpath is wrapped by every error [ObservedTopic] and
// [EventTopic] return when subpath is malformed.
var ErrInvalidSubpath = errors.New("mqttproto: invalid topic subpath")

// validateSubpath reports whether subpath is a well-formed, possibly
// multi-segment suffix for an observed/<subpath> or events/<subpath> topic.
func validateSubpath(subpath string) error {
	if subpath == "" {
		return fmt.Errorf("%w: subpath must not be empty", ErrInvalidSubpath)
	}
	if len(subpath) > maxSubpathLength {
		return fmt.Errorf("%w: %q: subpath exceeds %d characters", ErrInvalidSubpath, subpath, maxSubpathLength)
	}
	for _, seg := range strings.Split(subpath, "/") {
		if !subpathSegmentPattern.MatchString(seg) {
			return fmt.Errorf("%w: %q: every segment must match [a-z0-9][a-z0-9_-]*",
				ErrInvalidSubpath, subpath)
		}
	}
	return nil
}

// ErrInvalidCmdID is wrapped by every error [ResultTopic] returns when
// cmdID is malformed.
var ErrInvalidCmdID = errors.New("mqttproto: invalid command ID")

// validateCmdID reports whether cmdID is a well-formed single topic
// segment: this package does not otherwise constrain cmdID's real shape
// (in practice, pkg/command.Envelope.ID — a caller-chosen identifier,
// often a UUID); it only enforces that cmdID is one non-empty segment
// safe to place directly in a topic string.
func validateCmdID(cmdID string) error {
	if !subpathSegmentPattern.MatchString(cmdID) {
		return fmt.Errorf("%w: %q: must be one segment matching [a-z0-9][a-z0-9_-]*",
			ErrInvalidCmdID, cmdID)
	}
	return nil
}

// HelloTopic builds "showmesh/nodes/<node-id>/hello", the retained topic a
// node publishes its capability advertisement to.
func HelloTopic(nodeID string) (string, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return nodesPrefix + "/" + nodeID + "/" + segHello, nil
}

// LWTTopic builds "showmesh/nodes/<node-id>/lwt", the topic the broker
// publishes a node's registered Last Will to.
func LWTTopic(nodeID string) (string, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return nodesPrefix + "/" + nodeID + "/" + segLWT, nil
}

// CmdTopic builds "showmesh/nodes/<node-id>/cmd", the topic commands are
// sent to a node on. See [CmdPayload] for the payload this topic carries.
func CmdTopic(nodeID string) (string, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return nodesPrefix + "/" + nodeID + "/" + segCmd, nil
}

// ObservedTopic builds "showmesh/nodes/<node-id>/observed/<subpath>", the
// retained topic family for a node's observed state and health. subpath may
// itself contain '/' to express hierarchy (for example "health"), but each
// segment must be non-empty and free of MQTT wildcard characters.
func ObservedTopic(nodeID, subpath string) (string, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	if err := validateSubpath(subpath); err != nil {
		return "", err
	}
	return nodesPrefix + "/" + nodeID + "/" + segObserved + "/" + subpath, nil
}

// ResultTopic builds "showmesh/nodes/<node-id>/result/<cmd-id>", the topic
// a node publishes a command's result/evidence to. See [ResultPayload] for
// the payload this topic carries.
func ResultTopic(nodeID, cmdID string) (string, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	if err := validateCmdID(cmdID); err != nil {
		return "", err
	}
	return nodesPrefix + "/" + nodeID + "/" + segResult + "/" + cmdID, nil
}

// EventTopic builds "showmesh/events/<subpath>", the topic family for
// coordinator-published lifecycle and alert events.
func EventTopic(subpath string) (string, error) {
	if err := validateSubpath(subpath); err != nil {
		return "", err
	}
	return eventsPrefix + "/" + subpath, nil
}

// TopicKind discriminates the shape of a [Topic] returned by [ParseTopic].
type TopicKind int

const (
	// TopicKindUnknown is the zero value; ParseTopic never returns it on
	// success, only as part of the zero Topic alongside a non-nil error.
	TopicKindUnknown TopicKind = iota
	TopicKindHello
	TopicKindObserved
	TopicKindCmd
	TopicKindResult
	TopicKindLWT
	TopicKindEvent
)

// String renders known kinds by name and TopicKindUnknown, or any other
// value, as TopicKind(N).
func (k TopicKind) String() string {
	switch k {
	case TopicKindHello:
		return "Hello"
	case TopicKindObserved:
		return "Observed"
	case TopicKindCmd:
		return "Cmd"
	case TopicKindResult:
		return "Result"
	case TopicKindLWT:
		return "LWT"
	case TopicKindEvent:
		return "Event"
	default:
		return fmt.Sprintf("TopicKind(%d)", int(k))
	}
}

// Topic is the discriminated result of parsing an ADR-008 v1 topic string
// with [ParseTopic]. NodeID is set for every node-scoped kind (Hello,
// Observed, Cmd, Result, LWT) and empty for Event, which is not
// node-scoped. Subpath is set for Observed and Event. CmdID is set for
// Result. Fields not meaningful for a given Kind are left at their zero
// value.
type Topic struct {
	Kind    TopicKind
	NodeID  string
	Subpath string
	CmdID   string
}

// ErrInvalidTopic is wrapped by every error [ParseTopic] returns for a
// string that is not one of the ADR-008 v1 shapes.
var ErrInvalidTopic = errors.New("mqttproto: invalid topic")

// ErrTopicWildcard is wrapped by [ParseTopic] when topic contains an MQTT
// wildcard character ('+' or '#') anywhere. MQTT itself does not allow a
// broker to deliver a message on a topic name containing a wildcard (those
// characters are reserved for subscription filters), but ParseTopic checks
// for them explicitly and up front anyway, before any field extraction: the
// point is that a wildcard byte must never be able to flow through Topic
// and come back out looking like a validated node identity, regardless of
// what a future caller of this package does or does not trust about where
// a topic string came from.
var ErrTopicWildcard = errors.New("mqttproto: topic contains an MQTT wildcard character")

// ParseTopic parses topic against the exact ADR-008 v1 topic set and
// returns a discriminated [Topic]: kind, node ID, and any subpath or
// command ID. See [ErrTopicWildcard]'s doc comment for the wildcard
// rejection guarantee.
func ParseTopic(topic string) (Topic, error) {
	if strings.ContainsAny(topic, "+#") {
		return Topic{}, fmt.Errorf("%w: %q", ErrTopicWildcard, topic)
	}

	parts := strings.Split(topic, "/")

	if len(parts) >= 2 && parts[0] == "showmesh" && parts[1] == "events" {
		subpath := strings.Join(parts[2:], "/")
		if err := validateSubpath(subpath); err != nil {
			return Topic{}, err
		}
		return Topic{Kind: TopicKindEvent, Subpath: subpath}, nil
	}

	if len(parts) >= 4 && parts[0] == "showmesh" && parts[1] == "nodes" {
		nodeID := parts[2]
		if err := ValidateNodeID(nodeID); err != nil {
			return Topic{}, err
		}

		switch parts[3] {
		case segHello:
			if len(parts) != 4 {
				return Topic{}, fmt.Errorf("%w: %q: hello topic takes no further segments", ErrInvalidTopic, topic)
			}
			return Topic{Kind: TopicKindHello, NodeID: nodeID}, nil

		case segLWT:
			if len(parts) != 4 {
				return Topic{}, fmt.Errorf("%w: %q: lwt topic takes no further segments", ErrInvalidTopic, topic)
			}
			return Topic{Kind: TopicKindLWT, NodeID: nodeID}, nil

		case segCmd:
			if len(parts) != 4 {
				return Topic{}, fmt.Errorf("%w: %q: cmd topic takes no further segments", ErrInvalidTopic, topic)
			}
			return Topic{Kind: TopicKindCmd, NodeID: nodeID}, nil

		case segObserved:
			if len(parts) < 5 {
				return Topic{}, fmt.Errorf("%w: %q: observed topic requires a subpath", ErrInvalidTopic, topic)
			}
			subpath := strings.Join(parts[4:], "/")
			if err := validateSubpath(subpath); err != nil {
				return Topic{}, err
			}
			return Topic{Kind: TopicKindObserved, NodeID: nodeID, Subpath: subpath}, nil

		case segResult:
			if len(parts) != 5 {
				return Topic{}, fmt.Errorf("%w: %q: result topic requires exactly one cmd-id segment", ErrInvalidTopic, topic)
			}
			cmdID := parts[4]
			if err := validateCmdID(cmdID); err != nil {
				return Topic{}, err
			}
			return Topic{Kind: TopicKindResult, NodeID: nodeID, CmdID: cmdID}, nil
		}
	}

	return Topic{}, fmt.Errorf("%w: %q", ErrInvalidTopic, topic)
}
