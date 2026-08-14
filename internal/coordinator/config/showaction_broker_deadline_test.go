package config_test

// This file is deliberately package config_test (external), not package
// config: it is the ONLY place in this builder's own deliverables that
// imports both internal/coordinator/config and internal/coordinator/broker
// in the same compilation unit, which an internal config_test file could
// not do without recreating the exact import cycle showaction.go's own doc
// comment on mqttExpectMaxDeadlineSeconds explains (broker already imports
// config). An external test package is never imported by anything else, so
// it can hold both edges safely.

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestMQTTExpectMaxDeadlineSecondsAgreesWithBrokerMaxResponseDeadline is
// the test the wave 2 shared contract section 3 requires in place of the
// import this builder judged unacceptable (see showaction.go's own doc
// comment on mqttExpectMaxDeadlineSeconds): STEP-9-SPEC.md section 7.3's
// 120-second cap on target.expect.deadlineSeconds and
// broker.MaxResponseDeadline must stay the same number, and this test is
// what actually enforces that rather than leaving it as a comment's claim.
func TestMQTTExpectMaxDeadlineSecondsAgreesWithBrokerMaxResponseDeadline(t *testing.T) {
	// mqttExpectMaxDeadlineSeconds itself is unexported, so this test goes
	// through the one place its value is observable from outside the
	// package: DecodeShowActionPayload's own boundary behavior at the cap
	// and one past it, against a real internal/coordinator/broker constant
	// rather than a second hardcoded 120 literal in this file.
	wantMax := int(broker.MaxResponseDeadline / time.Second)

	registry := fakeDeadlineTestRegistry{}
	endpoints := []config.FPPEndpoint{{ID: "fpp-main", URL: "http://10.0.1.20"}}
	brokers := []config.IntegrationBroker{{ID: "home-automation", URL: "tcp://10.0.0.5:1883"}}

	mk := func(deadline int) string {
		return `{
			"show": "halloween-2026", "label": "x", "safetyClass": "none",
			"target": {
				"integration": "mqtt", "broker": "home-automation",
				"publish": {"topic": "t", "payload": "ON", "qos": 1},
				"expect": {"kind": "boolean", "topic": "t2", "deadlineSeconds": ` + strconv.Itoa(deadline) + `}
			}
		}`
	}

	if _, verr := config.DecodeShowActionPayload(mk(wantMax), endpoints, brokers, registry); verr != nil {
		t.Fatalf("deadline == broker.MaxResponseDeadline (%ds) must be accepted, got %+v", wantMax, verr)
	}
	if _, verr := config.DecodeShowActionPayload(mk(wantMax+1), endpoints, brokers, registry); verr == nil {
		t.Fatalf("deadline == broker.MaxResponseDeadline+1 (%ds) must be rejected, got no error", wantMax+1)
	}
}

// fakeDeadlineTestRegistry is a minimal config.FPPPrimitiveRegistry stand-in
// for this file only — this test never exercises an "fpp" target, so every
// method beyond satisfying the interface is unreached.
type fakeDeadlineTestRegistry struct{}

func (fakeDeadlineTestRegistry) DecodeActionParams(string, map[string]json.RawMessage) (map[string]any, error) {
	panic("not used by this test: it exercises an mqtt target only")
}

func (fakeDeadlineTestRegistry) Decision11Class(string) (string, bool) {
	panic("not used by this test: it exercises an mqtt target only")
}

func (fakeDeadlineTestRegistry) WireActions() []string {
	return nil
}
