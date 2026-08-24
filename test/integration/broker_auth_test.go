//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file exercises ADR-024 decision 10 against the exact shipped
// deploy/mosquitto/mosquitto.conf and deploy/mosquitto/acl.conf
// (scripts/test-integration.sh mounts both, unmodified — see that file's
// comment) with a real Mosquitto broker, real coordinator and agent
// subprocesses, and — for the ACL-boundary tests — a real, minimal MQTT
// client speaking directly to the broker so this suite proves what the ACL
// grants and denies, not only that credentials are checked at all.

// rawConnect dials brokerURL and performs one MQTT CONNECT with
// username/password, failing t if the broker does not answer with a
// success CONNACK. Callers must Disconnect (or just close the returned
// net.Conn indirectly by letting the test process exit) when done; every
// caller below does so via t.Cleanup.
func rawConnect(t *testing.T, username, password string) *paho.Client {
	t.Helper()

	u, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatalf("parse broker URL %q: %v", brokerURL, err)
	}

	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", u.Host, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cli := paho.NewClient(paho.ClientConfig{Conn: conn})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ack, err := cli.Connect(ctx, &paho.Connect{
		ClientID:     "showmesh-test-raw-" + uniqueSuffix(),
		UsernameFlag: true,
		Username:     username,
		PasswordFlag: true,
		Password:     []byte(password),
		KeepAlive:    30,
		CleanStart:   true,
	})
	if err != nil {
		t.Fatalf("CONNECT as %q: %v", username, err)
	}
	if ack.ReasonCode != 0 {
		t.Fatalf("CONNECT as %q: broker refused with reason code %d, want a successful connection for this test's setup", username, ack.ReasonCode)
	}

	t.Cleanup(func() { _ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) })
	return cli
}

// rawPublishReasonCode publishes an empty QoS 1 payload to topic on cli and
// returns the PUBACK reason code the broker answered with (0 == accepted;
// per MQTT v5, >= 0x80 == rejected, and 0x87 specifically means "not
// authorized" — see packets.PubackNotAuthorized). Fails t only on a
// transport-level problem (the PUBACK never arriving at all), never on a
// non-zero reason code: a rejection is exactly what several tests below
// are asserting on.
func rawPublishReasonCode(t *testing.T, cli *paho.Client, topic string) byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cli.Publish(ctx, &paho.Publish{QoS: 1, Topic: topic, Payload: []byte("x")})
	if resp != nil {
		return resp.ReasonCode
	}
	if err != nil {
		// publishQoS12 (paho/client.go) returns a nil *PublishResponse only
		// for a genuine transport/timeout failure, never for a rejecting
		// PUBACK (which always carries a non-nil response with the reason
		// code set) — so this path means the PUBACK never arrived at all,
		// which is a harness/broker problem this test cannot proceed past,
		// not an ACL result to assert on.
		t.Fatalf("PUBLISH %q: no PUBACK received at all (transport-level failure, not an ACL result): %v", topic, err)
	}
	t.Fatalf("PUBLISH %q: nil response and nil error, want one or the other", topic)
	return 0
}

func rawPublishRetainedReasonCode(t *testing.T, cli *paho.Client, topic string) byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cli.Publish(ctx, &paho.Publish{QoS: 1, Retain: true, Topic: topic, Payload: []byte("acl-regression")})
	if resp != nil {
		return resp.ReasonCode
	}
	if err != nil {
		t.Fatalf("retained PUBLISH %q: no PUBACK received at all (transport-level failure, not an ACL result): %v", topic, err)
	}
	t.Fatalf("retained PUBLISH %q: nil response and nil error, want one or the other", topic)
	return 0
}

// rawSubscribeReasonCode subscribes cli to topic and returns the SUBACK
// reason code (0-2 == accepted at that QoS; >= 0x80 == rejected).
func rawSubscribeReasonCode(t *testing.T, cli *paho.Client, topic string) byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sa, err := cli.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1}}})
	if sa != nil && len(sa.Reasons) == 1 {
		return sa.Reasons[0]
	}
	if err != nil {
		t.Fatalf("SUBSCRIBE %q: no SUBACK received at all: %v", topic, err)
	}
	t.Fatalf("SUBSCRIBE %q: no reason code in response", topic)
	return 0
}

// rawSubscribeReceives subscribes to topic and reports whether one publish
// arrives within timeout. Mosquitto may accept a broad subscription filter
// and suppress individual deliveries that ACL disallows, so an accepted
// SUBACK alone is not evidence that the caller can read the topic.
func rawSubscribeReceives(t *testing.T, cli *paho.Client, topic string, timeout time.Duration) bool {
	t.Helper()
	received := make(chan struct{}, 1)
	remove := cli.AddOnPublishReceived(func(pr paho.PublishReceived) (bool, error) {
		if pr.Packet != nil && pr.Packet.Topic == topic {
			select {
			case received <- struct{}{}:
			default:
			}
		}
		return true, nil
	})
	t.Cleanup(remove)

	if rc := rawSubscribeReasonCode(t, cli, topic); rc >= 0x80 {
		return false
	}
	select {
	case <-received:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestCoordinatorSurvivesBrokerRejectionAndReportsDistinctReadiness is
// deliverable 5's own acceptance test: the coordinator must start and stay
// up when the broker actively REJECTS its credential (CLAUDE.md's standing
// constraint 13, "starts with no broker reachable," extended by ADR-024
// decision 10 to "and now also when rejected" — see the build task's own
// framing). It also proves the Readiness-level half of "surface it as
// evidence distinct from an unreachable broker": /readyz's JSON reason must
// name the rejection, not read like an ordinary outage.
func TestCoordinatorSurvivesBrokerRejectionAndReportsDistinctReadiness(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL/passwd broker from `make test-integration`", envMosquittoContainer)
	}

	dataDir := t.TempDir()
	// A syntactically plausible but never-provisioned credential: the
	// broker has no passwd entry for this username at all, so this
	// specifically exercises CONNACK 0x86 Bad Username or Password (or
	// 0x87 Not Authorized — Mosquitto's exact choice between the two for
	// an unknown username is a broker implementation detail this test
	// does not pin down), not any other failure mode.
	tc := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir:      dataDir,
		clientID:     "showmesh-coordinator-test-rejected-" + uniqueSuffix(),
		mqttUsername: "coordinator-wrong-" + uniqueSuffix(),
		mqttPassword: "definitely-not-the-right-password",
	})

	// startCoordinatorWithConfig already blocked until /healthz answered
	// 200 — which is this test's first, load-bearing assertion, even
	// though it happened inside that helper: liveness must never depend on
	// broker auth succeeding (the standing constraint this test exists to
	// prove). Re-confirm it explicitly here anyway so a reader sees the
	// claim made directly in this test, not only implied by a helper not
	// failing.
	if healthzStatus, _ := tc.getRaw(t, "/healthz"); healthzStatus != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200: a coordinator whose broker credential was rejected must still report liveness — a credential rejection must never affect it", healthzStatus)
	}

	status, body := tc.getRaw(t, "/readyz")
	if status == http.StatusOK {
		t.Fatalf("/readyz = 200, want not-ready: this coordinator's broker credential was never valid, so it must never report ready")
	}

	var readyz map[string]any
	if err := json.Unmarshal(body, &readyz); err != nil {
		t.Fatalf("decode /readyz body: %v; body: %s", err, body)
	}

	reason, _ := readyz["reason"].(string)
	if !strings.Contains(reason, "rejected") || !strings.Contains(reason, "not authorized") {
		t.Errorf("/readyz reason = %q, want it to name the rejection distinctly (containing both \"rejected\" and \"not authorized\")", reason)
	}
	if reason == "mqtt broker not connected" {
		t.Fatalf("/readyz reason = %q, want it distinct from the plain unreachable-broker reason — that is the whole point of this test", reason)
	}
	if rejected, _ := readyz["rejected"].(bool); !rejected {
		t.Errorf("/readyz body[rejected] = %v, want true", readyz["rejected"])
	}
}

// TestAgentGivenWrongCredentialReportsDistinctEvidenceNotFatal is the other
// half of deliverable 4/5: an agent whose broker credential is rejected
// must not exit, and must log something that names the rejection
// specifically — not merely "the process is still running after N
// seconds", which (per this task's own testing standard) would pass
// identically if the agent had simply never attempted to connect at all.
// The assertion below is on log CONTENT naming the CONNACK rejection, which
// only a real connection attempt against the real broker can produce.
func TestAgentGivenWrongCredentialReportsDistinctEvidenceNotFatal(t *testing.T) {
	requireBroker(t)

	nodeID := "wrongcred-" + uniqueSuffix()
	a := startAgent(t, agentConfig{
		nodeID: nodeID,
		// Deliberately never provisioned: this username has no passwd
		// entry on the broker at all.
		mqttUsername: nodeID,
		mqttPassword: "wrong-password-" + uniqueSuffix(),
	})

	// Wait for the specific rejection log line to appear, bounded — not a
	// fixed sleep, and not merely "did the process exit" (which a crash
	// would also satisfy the negation of). Polling the subprocess's own
	// captured output is the harness's only window into what the agent
	// actually observed.
	deadline := time.Now().Add(10 * time.Second)
	var logs string
	for {
		logs = a.logs.String()
		if strings.Contains(logs, "not authorized") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not log a distinct authorization-rejection message within 10s; output so far:\n%s", logs)
		}
		select {
		case <-a.waitDone:
			t.Fatalf("agent exited (err=%v) instead of continuing to retry a rejected credential; output:\n%s", a.waitErr, a.logs.String())
		case <-time.After(50 * time.Millisecond):
		}
	}

	if !strings.Contains(logs, "permanent condition") {
		t.Errorf("agent log does not describe the rejection as a permanent condition (distinct from a transient network fault); output:\n%s", logs)
	}

	// The negative half of "not fatal": confirm the process is STILL
	// running some time after the rejection was logged, not merely that it
	// had not yet exited at the moment the log line appeared (a process
	// that logs the line and then immediately exits would pass a check
	// that only looked at the log).
	select {
	case <-a.waitDone:
		t.Fatalf("agent exited (err=%v) after logging the rejection; it must keep running and keep retrying", a.waitErr)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestAgentImpersonatingAnotherNodeGetsACLDeniedPublish proves
// mosquitto/acl.conf's per-agent `pattern` rules actually bind publish
// access to the AUTHENTICATED USERNAME, not to whatever node id the agent
// claims in its own configuration and topics — the property ADR-024
// decision 10's %u-not-%c rule exists to guarantee, and the one this
// task's own build prompt named as security-relevant rather than cosmetic.
// The agent here is authenticated as one provisioned identity but
// configured (SHOWMESH_NODE_ID) as a DIFFERENT node, so every publish it
// attempts lands outside its authenticated prefix and must be denied —
// this is the "quieter" ACL-denial-on-publish shape ADR-024 decision 10
// describes (the connection succeeds; only the publish is refused), and
// the agent must surface THAT distinctly too, not just a CONNACK
// rejection.
func TestAgentImpersonatingAnotherNodeGetsACLDeniedPublish(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}

	credentialOwner := "cred-owner-" + uniqueSuffix()
	claimedNodeID := "claimed-node-" + uniqueSuffix()
	username, password := provisionBrokerCredential(t, credentialOwner)

	a := startAgent(t, agentConfig{
		nodeID:       claimedNodeID,
		mqttUsername: username,
		mqttPassword: password,
	})

	deadline := time.Now().Add(10 * time.Second)
	var logs string
	for {
		logs = a.logs.String()
		if strings.Contains(logs, "rejected by broker ACL") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent authenticated as %q but claiming node id %q did not log an ACL-denied publish within 10s; output so far:\n%s",
				credentialOwner, claimedNodeID, logs)
		}
		select {
		case <-a.waitDone:
			t.Fatalf("agent exited (err=%v) instead of continuing after an ACL-denied publish; output:\n%s", a.waitErr, a.logs.String())
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Must NOT contain the CONNACK-rejection message: the connection
	// succeeded (the credential is real and valid), only the publish was
	// denied. Conflating the two would mean an operator cannot tell "wrong
	// password" apart from "right password, wrong node claimed" — a
	// provisioning mistake with a very different fix.
	if strings.Contains(logs, "mqtt broker rejected connection") {
		t.Errorf("agent log contains the CONNACK-rejection message even though the CONNECT succeeded with a valid credential; the ACL-denied-publish case must be logged distinctly from a CONNACK rejection. Output:\n%s", logs)
	}
}

// TestProvisionedAgentCredentialCannotPublishToOwnCmdTopic proves the
// carve-out in mosquitto/acl.conf's per-agent pattern rules: an agent's own
// credential grants publish on hello/lwt/observed/result beneath its own
// node prefix, but NOT on that same node's own cmd topic — ADR-024 decision
// 10 states unconditionally that "no client other than the coordinator may
// publish to any cmd topic", which a single blanket
// `showmesh/nodes/%u/#` pattern would have violated for a node's OWN cmd
// topic specifically. This talks to the broker directly (not through the
// agent binary, which never attempts this publish) because it is
// specifically testing what the ACL denies, not what the agent's own code
// happens to attempt.
func TestProvisionedAgentCredentialCannotPublishToOwnCmdTopic(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}

	nodeID := "cmdcarveout-" + uniqueSuffix()
	username, password := provisionBrokerCredential(t, nodeID)
	cli := rawConnect(t, username, password)

	// Sanity: this credential CAN publish beneath its own prefix (proves
	// the negative result below is the ACL's cmd carve-out specifically,
	// not a broken credential or a broken connection).
	if rc := rawPublishReasonCode(t, cli, "showmesh/nodes/"+nodeID+"/hello"); rc >= 0x80 {
		t.Fatalf("PUBLISH to own hello topic: reason code %d, want < 0x80 (accepted) — if this fails, the failure below is not meaningful", rc)
	}

	if rc := rawPublishReasonCode(t, cli, "showmesh/nodes/"+nodeID+"/cmd"); rc < 0x80 {
		t.Errorf("PUBLISH to own cmd topic: reason code %d, want >= 0x80 (rejected) — an agent must never be able to publish to its own cmd topic; only the coordinator may (ADR-024 decision 10)", rc)
	}
}

// TestCoordinatorCredentialCanPublishToAnyCmdTopicAndOnlyCoordinatorCan is
// the positive-and-negative pair for the coordinator's own ACL grant:
// exactly one credential on this broker may publish to any node's cmd
// topic, and it is the coordinator's.
func TestCoordinatorCredentialCanPublishToAnyCmdTopicAndOnlyCoordinatorCan(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}
	if testMQTTCoordinatorUsername == "" {
		t.Skipf("%s is not set", envTestMQTTCoordinatorUsername)
	}

	coordCli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	someNode := "some-node-" + uniqueSuffix()
	if rc := rawPublishReasonCode(t, coordCli, "showmesh/nodes/"+someNode+"/cmd"); rc >= 0x80 {
		t.Errorf("coordinator PUBLISH to %s/cmd: reason code %d, want < 0x80 (accepted) — the coordinator must be able to publish to any node's cmd topic", someNode, rc)
	}

	// An unrelated, non-coordinator credential must not gain the same
	// access merely by trying a node id that happens to match nothing in
	// particular.
	otherNode := "other-node-" + uniqueSuffix()
	otherUsername, otherPassword := provisionBrokerCredential(t, otherNode)
	otherCli := rawConnect(t, otherUsername, otherPassword)
	if rc := rawPublishReasonCode(t, otherCli, "showmesh/nodes/"+someNode+"/cmd"); rc < 0x80 {
		t.Errorf("non-coordinator PUBLISH to another node's cmd topic: reason code %d, want >= 0x80 (rejected)", rc)
	}
}

// TestFixedRolesDoNotInheritAgentTopicAccess is the regression for
// Mosquitto's global `pattern` behavior. A `pattern ... %u ...` grant applies
// to coordinator, fpp, and healthcheck too; the effective ACL must instead
// contain explicit user blocks for agents only. The paths deliberately use
// the fixed usernames themselves, which would have passed under the former
// global pattern rules and therefore prove the boundary directly.
func TestFixedRolesDoNotInheritAgentTopicAccess(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}
	if testMQTTCoordinatorUsername == "" {
		t.Skipf("%s is not set", envTestMQTTCoordinatorUsername)
	}

	fppUsername, fppPassword := provisionBrokerCredential(t, "fpp")
	healthcheckUsername, healthcheckPassword := provisionBrokerCredential(t, "healthcheck")
	roles := []struct {
		username string
		password string
	}{
		{testMQTTCoordinatorUsername, testMQTTCoordinatorPassword},
		{fppUsername, fppPassword},
		{healthcheckUsername, healthcheckPassword},
	}

	for _, role := range roles {
		role := role
		t.Run(role.username, func(t *testing.T) {
			cli := rawConnect(t, role.username, role.password)
			lwtTopic := "showmesh/nodes/" + role.username + "/lwt"
			if rc := rawPublishReasonCode(t, cli, lwtTopic); rc < 0x80 {
				t.Errorf("%s PUBLISH to %q: reason code %d, want >= 0x80 (rejected): a fixed role must not inherit the agent lwt write grant", role.username, lwtTopic, rc)
			}

			// Mosquitto can return a successful SUBACK for a filter but omit
			// deliveries it is not authorized to read. Publish a retained,
			// unique command through the coordinator (the sole authorized
			// writer) and prove this fixed-role subscriber never receives it.
			cmdTopic := "showmesh/nodes/" + role.username + "/cmd"
			publisher := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
			if rc := rawPublishRetainedReasonCode(t, publisher, cmdTopic); rc >= 0x80 {
				t.Fatalf("coordinator PUBLISH to %q: reason code %d, want < 0x80 to set up fixed-role read denial check", cmdTopic, rc)
			}
			if rawSubscribeReceives(t, cli, cmdTopic, 300*time.Millisecond) {
				t.Errorf("%s received a command published to %q: a fixed role must not inherit the agent cmd read grant", role.username, cmdTopic)
			}
		})
	}

	// Control the observation mechanism itself: a provisioned agent must
	// receive the same retained command on its own permitted cmd topic.
	agentID := "acl-read-control-" + uniqueSuffix()
	agentUsername, agentPassword := provisionBrokerCredential(t, agentID)
	publisher := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	controlTopic := "showmesh/nodes/" + agentID + "/cmd"
	if rc := rawPublishRetainedReasonCode(t, publisher, controlTopic); rc >= 0x80 {
		t.Fatalf("coordinator retained PUBLISH to %q: reason code %d, want < 0x80", controlTopic, rc)
	}
	if !rawSubscribeReceives(t, rawConnect(t, agentUsername, agentPassword), controlTopic, 2*time.Second) {
		t.Fatalf("provisioned agent did not receive retained command on %q; the fixed-role no-delivery checks above are not meaningful", controlTopic)
	}
}

// TestFPPPublisherRoleHasNoShowmeshAccess and
// TestHealthcheckPrincipalIsReadOnlySYS cover the two ACL roles this
// task's ACL file grants beyond agents and the coordinator (ADR-024
// decision 10's "an FPP publisher role" and "a healthcheck principal").
func TestFPPPublisherRoleHasNoShowmeshAccess(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}

	username, password := provisionBrokerCredential(t, "fpp")
	cli := rawConnect(t, username, password)

	if rc := rawPublishReasonCode(t, cli, "falcon/player/FPP-Test/status"); rc >= 0x80 {
		t.Errorf("fpp role PUBLISH to its own status prefix: reason code %d, want < 0x80 (accepted)", rc)
	}
	if rc := rawPublishReasonCode(t, cli, "showmesh/nodes/anything/hello"); rc < 0x80 {
		t.Errorf("fpp role PUBLISH to a showmesh/ topic: reason code %d, want >= 0x80 (rejected) — the fpp role must have no showmesh/ access at all", rc)
	}
}

// TestFPPPublisherRoleCannotWriteCommandTopicButCanSubscribeToIt closes
// review finding 7: deploy/mosquitto/acl.conf's fpp role used to grant
// "topic readwrite falcon/player/#", which — because MQTT wildcards do not
// distinguish "status" from "command" — also granted WRITE on
// falcon/player/<host>/command/run, the exact topic CLAUDE.md names by
// name as "a topic FPP acts on" (Step 5's own "GET-only is not read-only"
// lesson's MQTT analogue). ADR-024 decision 10 confines this role to
// FPP's own status topics: write is now enumerated per suffix
// (TestFPPPublisherRoleHasNoShowmeshAccess above already proves one of
// those suffixes, "status", is still writable), and the command subtree
// stays READ-only — FPP itself subscribes to its own command topic to
// receive commands sent to it, so removing read there too would silently
// break FPP's own remote-command feature on any operator who followed
// ADR-008's one-broker recommendation, which is not what this fix is for.
func TestFPPPublisherRoleCannotWriteCommandTopicButCanSubscribeToIt(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}

	username, password := provisionBrokerCredential(t, "fpp")
	cli := rawConnect(t, username, password)

	if rc := rawPublishReasonCode(t, cli, "falcon/player/FPP-Test/command/run"); rc < 0x80 {
		t.Errorf("fpp role PUBLISH to its own command/run topic: reason code %d, want >= 0x80 (rejected) — this is the exact topic FPP acts on; the fpp role must never be able to write it", rc)
	}
	if rc := rawSubscribeReasonCode(t, cli, "falcon/player/FPP-Test/command/run"); rc >= 0x80 {
		t.Errorf("fpp role SUBSCRIBE to its own command/run topic: reason code %d, want < 0x80 (accepted) — FPP itself receives commands via this subtree, which must stay readable", rc)
	}
}

func TestHealthcheckPrincipalIsReadOnlySYS(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}

	username, password := provisionBrokerCredential(t, "healthcheck")
	cli := rawConnect(t, username, password)

	if rc := rawSubscribeReasonCode(t, cli, "$SYS/#"); rc >= 0x80 {
		t.Errorf("healthcheck principal SUBSCRIBE to $SYS/#: reason code %d, want < 0x80 (accepted) — this is exactly what the container healthcheck depends on", rc)
	}
	if rc := rawPublishReasonCode(t, cli, "showmesh/nodes/anything/hello"); rc < 0x80 {
		t.Errorf("healthcheck principal PUBLISH to a showmesh/ topic: reason code %d, want >= 0x80 (rejected) — this principal must be read-only on $SYS and nothing else", rc)
	}
}

// TestTwoProvisionedAgentsAppearInInventory is the positive-path sanity
// check the negative ACL tests above need: proves the shipped
// allow_anonymous=false + acl.conf posture does not accidentally break
// ordinary operation for two simultaneous, correctly provisioned agents —
// the same "two real agent subprocesses" shape deliverable 6's acceptance
// criterion names for the full Compose bundle, exercised here against the
// identical shipped mosquitto.conf/acl.conf rather than only asserted by
// hand against the Compose stack.
func TestTwoProvisionedAgentsAppearInInventory(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	tc := startCoordinator(t, dataDir, "showmesh-coordinator-test-twoagents-"+uniqueSuffix())

	nodeA := "twoagents-a-" + uniqueSuffix()
	nodeB := "twoagents-b-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeA})
	startAgent(t, agentConfig{nodeID: nodeB})

	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		_, okA := tc.findNode(t, nodeA)
		_, okB := tc.findNode(t, nodeB)
		return okA && okB
	}, fmt.Sprintf("both %s and %s to appear in coordinator inventory", nodeA, nodeB))
}

// TestAgentReadsShowModeTopicButCannotPublishIt proves the one events
// topic a node may see is readable and read-only. Without the read grant
// the retained mode never reaches a node, which then reads unknown and
// behaves as show forever with nothing saying why; with a write grant a
// node could tell the installation what mode it is in, which ADR-033
// forbids (the agent derives the mode from nothing and publishes none).
func TestAgentReadsShowModeTopicButCannotPublishIt(t *testing.T) {
	requireBroker(t)
	if mosquittoContainer == "" {
		t.Skipf("%s is not set; this test needs the real shipped ACL from `make test-integration`", envMosquittoContainer)
	}

	agentID := "acl-show-mode-" + uniqueSuffix()
	username, password := provisionBrokerCredential(t, agentID)
	agent := rawConnect(t, username, password)

	topic := mqttproto.ShowModeTopic()
	publisher := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	if rc := rawPublishRetainedReasonCode(t, publisher, topic); rc >= 0x80 {
		t.Fatalf("coordinator retained PUBLISH to %q: reason code %d, want < 0x80", topic, rc)
	}
	if !rawSubscribeReceives(t, agent, topic, 2*time.Second) {
		t.Errorf("provisioned agent never received the retained message on %q: a node cannot be told the operating mode at all", topic)
	}
	if rc := rawPublishReasonCode(t, rawConnect(t, username, password), topic); rc < 0x80 {
		t.Errorf("agent PUBLISH to %q: reason code %d, want >= 0x80 (rejected): a node consumes the mode and never publishes one", topic, rc)
	}
}
