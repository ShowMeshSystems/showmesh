package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// nanosecondScaleInstant is past Number.MAX_SAFE_INTEGER (9007199254740991)
// on purpose: it is the magnitude a real media-clock reading has, and the
// magnitude at which a float64 decode silently rounds.
const nanosecondScaleInstant int64 = 1789000000123456789

// TestScheduledStartInstantSurvivesTheWireExactly is the end-to-end pin
// on the rounding defect this seam exists downstream of: a command
// carrying a nanosecond-scale start instant must reach the agent's own
// parser as the exact integer that was sent. A float64 anywhere on that
// path loses the low digits before any clock is consulted.
func TestScheduledStartInstantSurvivesTheWireExactly(t *testing.T) {
	cmd := mqttproto.CmdPayload{
		CommandID: "cmd-1", IdempotencyKey: "inv-1", Action: string(pkgaudio.OperationSessionStart),
		Target: mqttproto.CmdTarget{Kind: "node", ID: "node-1"},
		Params: map[string]any{
			"sessionId":                 "s1",
			"invocationId":              "inv-1",
			"revision":                  2,
			pkgaudio.ParamScheduledAtNs: nanosecondScaleInstant,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "p", PrincipalName: "n"},
		ConfirmationMethod: "evidence",
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	decoded, err := mqttproto.DecodeCmdPayload(mqttproto.Envelope{Schema: mqttproto.SchemaNodeCmdV1, Payload: raw})
	if err != nil {
		t.Fatalf("DecodeCmdPayload: %v", err)
	}

	got, present, err := parseScheduledAtNs(decoded.Params)
	if err != nil {
		t.Fatalf("parseScheduledAtNs: %v", err)
	}
	if !present {
		t.Fatal("the start instant was not present after a wire round trip")
	}
	if got != nanosecondScaleInstant {
		t.Fatalf("start instant after a wire round trip = %d, want %d (off by %d nanoseconds: this is the float64 rounding this path exists to prevent)",
			got, nanosecondScaleInstant, got-nanosecondScaleInstant)
	}
}

func TestParseScheduledAtNsAbsentIsNotAnError(t *testing.T) {
	_, present, err := parseScheduledAtNs(map[string]any{"sessionId": "s1"})
	if err != nil {
		t.Fatalf("parseScheduledAtNs with no instant = %v, want no error: the param is optional", err)
	}
	if present {
		t.Fatal("reported an instant that was not in the params")
	}
}

// TestParseScheduledAtNsRefusesAnAlreadyRoundedFloat: a caller that hands
// this an inexact float64 has already lost the low digits, and starting
// against it would present a rounded instant as an exact one.
func TestParseScheduledAtNsRefusesAnAlreadyRoundedFloat(t *testing.T) {
	rounded := float64(nanosecondScaleInstant)
	if int64(rounded) == nanosecondScaleInstant {
		t.Skip("this platform's float64 holds the test instant exactly; nothing to prove")
	}
	if _, _, err := parseScheduledAtNs(map[string]any{pkgaudio.ParamScheduledAtNs: rounded}); err == nil {
		t.Fatal("a float64 that cannot represent the instant exactly was accepted")
	}
}

func TestParseScheduledAtNsRefusesANonNumber(t *testing.T) {
	if _, _, err := parseScheduledAtNs(map[string]any{pkgaudio.ParamScheduledAtNs: "soon"}); err == nil {
		t.Fatal("a non-numeric start instant was accepted")
	}
}

// TestPrepareReportsMediaClockReadiness proves the prepare result carries
// the reading a coordinator needs to pick a start instant, and that a
// node with no clock says so rather than omitting the field, which reads
// as fine.
func TestPrepareReportsMediaClockReadiness(t *testing.T) {
	mgr := audio.NewManager(nil, nil, "", nil, time.Now, nil)
	extra := map[string]any{}
	addMediaClockReadiness(context.Background(), mgr, extra)

	valid, ok := extra[pkgaudio.ResultMediaClockValid].(bool)
	if !ok {
		t.Fatalf("%s missing from a prepare result; an absent validity flag is read as valid", pkgaudio.ResultMediaClockValid)
	}
	if valid {
		t.Fatal("a Manager with no clock wired reported a valid media-clock reading")
	}
	if reason, _ := extra[pkgaudio.ResultMediaClockReason].(string); reason == "" {
		t.Fatalf("%s was empty on an invalid reading", pkgaudio.ResultMediaClockReason)
	}
	if _, present := extra[pkgaudio.ResultMediaClockNowNs]; present {
		t.Fatal("an invalid reading still carried an instant; it must carry none rather than a zero that reads as an epoch")
	}
}

// TestMediaClockReadingSurvivesTheResultWireExactly is the command test's
// mirror on the result direction: the coordinator adds a margin to this
// reading and sends it straight back as a start instant, so a float64
// here would round the schedule before it was ever chosen.
func TestMediaClockReadingSurvivesTheResultWireExactly(t *testing.T) {
	collectedAt := time.Unix(1_700_000_000, 0).UTC()
	result := mqttproto.ResultPayload{
		CommandID: "cmd-1", IdempotencyKey: "inv-1", Action: string(pkgaudio.OperationSessionPrepare),
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session.prepare",
			Value: map[string]any{
				"sessionId":                    "s1",
				pkgaudio.ResultMediaClockValid: true,
				pkgaudio.ResultMediaClockNowNs: nanosecondScaleInstant,
				pkgaudio.ResultPrerollMs:       int64(42),
			},
			CollectedAt: collectedAt,
		},
		ReceivedAt: collectedAt, RespondedAt: collectedAt,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	decoded, err := mqttproto.DecodeResultPayload(mqttproto.Envelope{Schema: mqttproto.SchemaNodeResultV1, Payload: raw})
	if err != nil {
		t.Fatalf("DecodeResultPayload: %v", err)
	}

	value, ok := decoded.Evidence.Value.(map[string]any)
	if !ok {
		t.Fatalf("evidence value decoded as %T, want an object", decoded.Evidence.Value)
	}
	number, ok := value[pkgaudio.ResultMediaClockNowNs].(json.Number)
	if !ok {
		t.Fatalf("%s decoded as %T, want json.Number: any other numeric type has already rounded it",
			pkgaudio.ResultMediaClockNowNs, value[pkgaudio.ResultMediaClockNowNs])
	}
	got, err := number.Int64()
	if err != nil {
		t.Fatalf("%s = %q, not an int64: %v", pkgaudio.ResultMediaClockNowNs, number, err)
	}
	if got != nanosecondScaleInstant {
		t.Fatalf("media-clock reading after a result round trip = %d, want %d (off by %d nanoseconds)",
			got, nanosecondScaleInstant, got-nanosecondScaleInstant)
	}
}
