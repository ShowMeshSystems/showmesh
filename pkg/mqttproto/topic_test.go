package mqttproto

import (
	"errors"
	"strings"
	"testing"
)

func TestTopicBuildParseRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		build func() (string, error)
		want  Topic
	}{
		{
			name:  "hello",
			build: func() (string, error) { return HelloTopic("media-03") },
			want:  Topic{Kind: TopicKindHello, NodeID: "media-03"},
		},
		{
			name:  "lwt",
			build: func() (string, error) { return LWTTopic("media-03") },
			want:  Topic{Kind: TopicKindLWT, NodeID: "media-03"},
		},
		{
			name:  "cmd",
			build: func() (string, error) { return CmdTopic("media-03") },
			want:  Topic{Kind: TopicKindCmd, NodeID: "media-03"},
		},
		{
			name:  "observed single segment subpath",
			build: func() (string, error) { return ObservedTopic("media-03", "health") },
			want:  Topic{Kind: TopicKindObserved, NodeID: "media-03", Subpath: "health"},
		},
		{
			name:  "observed multi segment subpath",
			build: func() (string, error) { return ObservedTopic("media-03", "health/detail") },
			want:  Topic{Kind: TopicKindObserved, NodeID: "media-03", Subpath: "health/detail"},
		},
		{
			name:  "result with cmd-id",
			build: func() (string, error) { return ResultTopic("media-03", "a1b2c3") },
			want:  Topic{Kind: TopicKindResult, NodeID: "media-03", CmdID: "a1b2c3"},
		},
		{
			name:  "event",
			build: func() (string, error) { return EventTopic("lifecycle") },
			want:  Topic{Kind: TopicKindEvent, Subpath: "lifecycle"},
		},
		{
			name:  "event multi segment",
			build: func() (string, error) { return EventTopic("alert/pixel-current") },
			want:  Topic{Kind: TopicKindEvent, Subpath: "alert/pixel-current"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topicStr, err := tt.build()
			if err != nil {
				t.Fatalf("build() error = %v", err)
			}
			got, err := ParseTopic(topicStr)
			if err != nil {
				t.Fatalf("ParseTopic(%q) error = %v", topicStr, err)
			}
			if got != tt.want {
				t.Fatalf("ParseTopic(%q) = %+v, want %+v", topicStr, got, tt.want)
			}
		})
	}
}

func TestValidateNodeIDRejections(t *testing.T) {
	tooLong := make([]byte, 65)
	for i := range tooLong {
		tooLong[i] = 'a'
	}

	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "slash", id: "media/03"},
		{name: "plus wildcard", id: "media+03"},
		{name: "hash wildcard", id: "media#"},
		{name: "dollar sign", id: "$media03"},
		{name: "whitespace", id: "media 03"},
		{name: "uppercase", id: "Media03"},
		{name: "non-ASCII", id: "médiå03"},
		{name: "leading hyphen", id: "-media03"},
		{name: "trailing hyphen", id: "media03-"},
		{name: "too long, 65 chars", id: string(tooLong)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNodeID(tt.id)
			if err == nil {
				t.Fatalf("ValidateNodeID(%q) = nil, want error", tt.id)
			}
			if !errors.Is(err, ErrInvalidNodeID) {
				t.Fatalf("ValidateNodeID(%q) error = %v, want errors.Is(err, ErrInvalidNodeID)", tt.id, err)
			}
		})
	}
}

func TestValidateNodeIDAccepts(t *testing.T) {
	tests := []string{
		"a",
		"media03",
		"media-03",
		"a-b-c",
		"09media",
	}
	for _, id := range tests {
		t.Run(id, func(t *testing.T) {
			if err := ValidateNodeID(id); err != nil {
				t.Fatalf("ValidateNodeID(%q) = %v, want nil", id, err)
			}
		})
	}

	// Exactly 64 characters must be accepted (the boundary itself).
	exactly64 := make([]byte, 64)
	for i := range exactly64 {
		exactly64[i] = 'a'
	}
	if err := ValidateNodeID(string(exactly64)); err != nil {
		t.Fatalf("ValidateNodeID(64 chars) = %v, want nil", err)
	}
}

func TestParseTopicRejectsWildcard(t *testing.T) {
	tests := []string{
		"showmesh/nodes/+/hello",
		"showmesh/nodes/media-03/observed/#",
		"showmesh/nodes/#/lwt",
		"showmesh/events/+",
	}
	for _, topic := range tests {
		t.Run(topic, func(t *testing.T) {
			got, err := ParseTopic(topic)
			if err == nil {
				t.Fatalf("ParseTopic(%q) = %+v, nil, want an error", topic, got)
			}
			if !errors.Is(err, ErrTopicWildcard) {
				t.Fatalf("ParseTopic(%q) error = %v, want errors.Is(err, ErrTopicWildcard)", topic, err)
			}
			// A wildcard must never round-trip into a node identity.
			if got.NodeID != "" {
				t.Fatalf("ParseTopic(%q) NodeID = %q on error, want empty", topic, got.NodeID)
			}
		})
	}
}

func TestParseTopicInvalidShapes(t *testing.T) {
	tests := []string{
		"",
		"showmesh",
		"showmesh/nodes",
		"showmesh/nodes/media-03",
		"showmesh/nodes/media-03/bogus",
		"showmesh/nodes/media-03/hello/extra",
		"showmesh/nodes/media-03/lwt/extra",
		"showmesh/nodes/media-03/cmd/extra",
		"showmesh/nodes/media-03/observed",
		"showmesh/nodes/media-03/result",
		"showmesh/nodes/media-03/result/cmd1/extra",
		"showmesh/nodes/Media03/hello", // invalid node ID
		"other/nodes/media-03/hello",
		"showmesh/events",
	}
	for _, topic := range tests {
		t.Run(topic, func(t *testing.T) {
			if _, err := ParseTopic(topic); err == nil {
				t.Fatalf("ParseTopic(%q) = nil error, want an error", topic)
			}
		})
	}
}

func TestSubscriptionFilters(t *testing.T) {
	if SubscribeHello != "showmesh/nodes/+/hello" {
		t.Fatalf("SubscribeHello = %q", SubscribeHello)
	}
	if SubscribeLWT != "showmesh/nodes/+/lwt" {
		t.Fatalf("SubscribeLWT = %q", SubscribeLWT)
	}
	if SubscribeObserved != "showmesh/nodes/+/observed/#" {
		t.Fatalf("SubscribeObserved = %q", SubscribeObserved)
	}
}

// TestTopicBuildersRejectBadNodeID proves every node-scoped topic builder,
// not just [ValidateNodeID] itself, rejects a malformed node ID -- including
// one carrying an MQTT wildcard character, which is the topic-injection
// case [nodeIDPattern]'s doc comment calls out by name.
func TestTopicBuildersRejectBadNodeID(t *testing.T) {
	builders := []struct {
		name  string
		build func(id string) (string, error)
	}{
		{name: "HelloTopic", build: HelloTopic},
		{name: "LWTTopic", build: LWTTopic},
		{name: "CmdTopic", build: CmdTopic},
		{name: "ObservedTopic", build: func(id string) (string, error) { return ObservedTopic(id, "health") }},
		{name: "ResultTopic", build: func(id string) (string, error) { return ResultTopic(id, "cmd1") }},
	}

	badIDs := []string{"", "Bad/ID", "media+03", "media#", "Media03"}

	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			for _, id := range badIDs {
				t.Run(id, func(t *testing.T) {
					if _, err := b.build(id); !errors.Is(err, ErrInvalidNodeID) {
						t.Fatalf("%s(%q) error = %v, want errors.Is(err, ErrInvalidNodeID)", b.name, id, err)
					}
				})
			}
		})
	}
}

func TestEventTopicRejectsBadSubpath(t *testing.T) {
	tests := []string{"", "lifecycle/", "/lifecycle", "lifecycle//detail", "li@fecycle", "li fecycle"}
	for _, subpath := range tests {
		t.Run(subpath, func(t *testing.T) {
			if _, err := EventTopic(subpath); err == nil {
				t.Fatalf("EventTopic(%q) = nil error, want error", subpath)
			} else if !errors.Is(err, ErrInvalidSubpath) {
				t.Fatalf("EventTopic(%q) error = %v, want errors.Is(err, ErrInvalidSubpath)", subpath, err)
			}
		})
	}
}

func TestSubpathRejectsTooLong(t *testing.T) {
	tooLong := strings.Repeat("a", maxSubpathLength+1)

	if _, err := ObservedTopic("media-03", tooLong); !errors.Is(err, ErrInvalidSubpath) {
		t.Fatalf("ObservedTopic(media-03, <%d chars>) error = %v, want errors.Is(err, ErrInvalidSubpath)", len(tooLong), err)
	}
	if _, err := EventTopic(tooLong); !errors.Is(err, ErrInvalidSubpath) {
		t.Fatalf("EventTopic(<%d chars>) error = %v, want errors.Is(err, ErrInvalidSubpath)", len(tooLong), err)
	}

	// The bound itself must still be accepted.
	exactly := strings.Repeat("a", maxSubpathLength)
	if _, err := ObservedTopic("media-03", exactly); err != nil {
		t.Fatalf("ObservedTopic(media-03, <%d chars>) error = %v, want nil", len(exactly), err)
	}
}

func TestObservedTopicRejectsBadSubpath(t *testing.T) {
	tests := []string{"", "health/", "/health", "health//detail", "he@lth", "he alth"}
	for _, subpath := range tests {
		t.Run(subpath, func(t *testing.T) {
			if _, err := ObservedTopic("media-03", subpath); err == nil {
				t.Fatalf("ObservedTopic(media-03, %q) = nil error, want error", subpath)
			}
		})
	}
}

func TestResultTopicRejectsBadCmdID(t *testing.T) {
	tests := []string{"", "a/b", "a+b", "a#b"}
	for _, cmdID := range tests {
		t.Run(cmdID, func(t *testing.T) {
			if _, err := ResultTopic("media-03", cmdID); err == nil {
				t.Fatalf("ResultTopic(media-03, %q) = nil error, want error", cmdID)
			}
		})
	}
}
