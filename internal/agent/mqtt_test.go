package agent

import (
	"errors"
	"testing"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// TestBuildWillMessageTopicRetainQoSAndPayload covers the Task D spec's
// "the will payload encodes correctly and is the right topic, retain, and
// QoS" requirement without dialing a broker: buildWillMessage is a pure
// function of nodeID (aside from wall-clock SentAt), so it is tested
// directly rather than through newMQTTConn (which does start a real
// autopaho connection attempt and is exercised only by the integration
// task per CLAUDE.md's Step 2 build order).
func TestBuildWillMessageTopicRetainQoSAndPayload(t *testing.T) {
	will, err := buildWillMessage("media-03")
	if err != nil {
		t.Fatalf("buildWillMessage() error = %v", err)
	}

	wantTopic, err := mqttproto.LWTTopic("media-03")
	if err != nil {
		t.Fatalf("LWTTopic() error = %v", err)
	}
	if will.Topic != wantTopic {
		t.Errorf("Topic = %q, want %q", will.Topic, wantTopic)
	}
	if will.QoS != mqttproto.LWTDeliveryPolicy.QoS {
		t.Errorf("QoS = %d, want %d", will.QoS, mqttproto.LWTDeliveryPolicy.QoS)
	}
	if will.Retain != mqttproto.LWTDeliveryPolicy.Retain {
		t.Errorf("Retain = %v, want %v (mqttproto.LWTDeliveryPolicy.Retain)", will.Retain, mqttproto.LWTDeliveryPolicy.Retain)
	}

	env, err := mqttproto.DecodeEnvelope(will.Payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope(will.Payload) error = %v", err)
	}
	if env.NodeID != "media-03" {
		t.Errorf("envelope NodeID = %q, want %q", env.NodeID, "media-03")
	}
	lwt, err := mqttproto.DecodeLWTPayload(env)
	if err != nil {
		t.Fatalf("DecodeLWTPayload() error = %v", err)
	}
	if lwt.Online {
		t.Errorf("Online = true, want false: a Last Will always represents 'not online'")
	}
	if lwt.Reason == "" {
		t.Errorf("Reason is empty, want a description of an unexpected disconnect")
	}
}

func TestBuildWillMessageInvalidNodeID(t *testing.T) {
	if _, err := buildWillMessage(""); err == nil {
		t.Fatalf("buildWillMessage(\"\") error = nil, want an error for an invalid node ID")
	}
}

// TestIsAuthReasonCode is the unit-testable half of ADR-024 decision 10's
// "surface v5 authorization reason codes distinctly": every reason code
// this project treats as "the broker knows who I am and refused me" must
// classify true, and everything else (success, and every non-authorization
// failure code a real broker can plausibly return) must classify false —
// in particular ConnackServerBusy and ConnackUnspecifiedError, which are
// exactly the kind of generic-looking failure code a less careful
// implementation might lump in with the authorization family.
func TestIsAuthReasonCode(t *testing.T) {
	authCodes := []byte{
		packets.ConnackBadUsernameOrPassword,
		packets.ConnackNotAuthorized,
		packets.ConnackBanned,
		packets.ConnackBadAuthenticationMethod,
	}
	for _, code := range authCodes {
		if !isAuthReasonCode(code) {
			t.Errorf("isAuthReasonCode(0x%02X) = false, want true", code)
		}
	}

	nonAuthCodes := []byte{
		packets.ConnackSuccess,
		packets.ConnackUnspecifiedError,
		packets.ConnackServerBusy,
		packets.ConnackServerUnavailable,
		packets.ConnackMalformedPacket,
	}
	for _, code := range nonAuthCodes {
		if isAuthReasonCode(code) {
			t.Errorf("isAuthReasonCode(0x%02X) = true, want false", code)
		}
	}
}

// TestClassifyConnectErrorAuthRejection is the direct test of the behavior
// this whole file's ADR-024 decision 10 change exists to produce: a CONNACK
// 0x87 Not Authorized (or one of its siblings) must classify as rejected,
// surfacing the reason code and any broker-supplied reason string. Also run
// against the same error joined under another one (errors.Join, not how
// autopaho/net.go's establishServerConnection actually delivers it today —
// today it hands OnConnectError the *ConnackError directly, unwrapped — but
// classifyConnectError uses errors.As specifically so it keeps working if a
// future paho.golang version, or a future ShowMesh wrapper, adds a layer;
// this case is what actually exercises that errors.As unwraps rather than
// requiring an exact top-level type).
func TestClassifyConnectErrorAuthRejection(t *testing.T) {
	base := &autopaho.ConnackError{
		ReasonCode: packets.ConnackNotAuthorized,
		Reason:     "client not authorized",
		Err:        errors.New("connect failed"),
	}
	wrapped := errors.Join(errors.New("failed to connect to tcp://broker:1883"), base)

	for name, err := range map[string]error{"unwrapped": base, "wrapped": wrapped} {
		t.Run(name, func(t *testing.T) {
			rejected, code, reason := classifyConnectError(err)
			if !rejected {
				t.Fatalf("rejected = false, want true for a ConnackNotAuthorized error")
			}
			if code != packets.ConnackNotAuthorized {
				t.Errorf("code = 0x%02X, want 0x%02X", code, packets.ConnackNotAuthorized)
			}
			if reason != "client not authorized" {
				t.Errorf("reason = %q, want the CONNACK reason string carried through", reason)
			}
		})
	}
}

// TestClassifyConnectErrorNonAuthConnack proves classifyConnectError does
// not conflate every CONNACK failure with an authorization rejection: a
// broker that received the CONNECT but is merely busy is a very different,
// plausibly-transient condition from one that refused the credential.
func TestClassifyConnectErrorNonAuthConnack(t *testing.T) {
	err := &autopaho.ConnackError{ReasonCode: packets.ConnackServerBusy, Err: errors.New("connect failed")}

	rejected, _, _ := classifyConnectError(err)
	if rejected {
		t.Errorf("rejected = true for ConnackServerBusy, want false: a busy broker is not an authorization rejection")
	}
}

// TestClassifyConnectErrorTransportFailure proves the case that matters
// most for CLAUDE.md's Step 5-derived "GET-only is not read-only" family of
// lessons applied here: a connection that never reached CONNACK at all
// (refused, DNS failure, timeout — none of these carry a paho.Connack)
// must never be misclassified as an authorization rejection. Getting this
// backward would be worse than not classifying at all: it would make a
// genuinely transient network fault log as "permanent condition, will not
// resolve by retrying," which is exactly the wrong signal to hand an
// operator chasing a fault during a show.
func TestClassifyConnectErrorTransportFailure(t *testing.T) {
	err := errors.New("dial tcp 10.0.1.5:1883: connect: connection refused")

	rejected, code, reason := classifyConnectError(err)
	if rejected {
		t.Errorf("rejected = true for a plain transport error, want false")
	}
	if code != 0 || reason != "" {
		t.Errorf("code/reason = %d/%q, want zero values when no CONNACK was ever received", code, reason)
	}
}
