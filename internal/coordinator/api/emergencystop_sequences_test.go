package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const estopSequencesBody = `{"shutdownSequences":[
	{"id":"projectors","label":"Projectors","steps":[
		{"action":"lamps-off","requireConfirmation":true},
		{"delayMs":3600000},
		{"action":"worklights-on","powerDomain":"presentation","domainProvenance":"operator-declared"}
	]},
	{"id":"fm","label":"FM transmitter","steps":[{"action":"lamps-off"}]}
]}`

// Hard stop sends every step of the sequences it includes at once, past an
// hour-long wait, and leaves out the ones it skips.
func TestHardStopSendsEveryIncludedStepWithoutWaiting(t *testing.T) {
	f := newEmergencyStopFixture(t, testNow)
	mustPutAction(t, f.api, f.adminToken, "lamps-off", validShowActionResolumeBlackoutBody)
	if code, body := f.putSitePower(t, estopSequencesBody); code != http.StatusOK {
		t.Fatalf("PUT show.sitepower: %d %s", code, body)
	}
	f.putConfig(t, `{"stop":{"actions":[]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[],"skipSequences":["fm"]}}`)
	before := len(f.resolumeActionsSent())

	auth := map[string]string{"Authorization": "Bearer " + f.adminToken}
	_, armBody := doRawRequest(t, f.api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"hs-seq-arm"}`, auth))
	token := strings.Split(strings.Split(string(armBody), `"armToken":"`)[1], `"`)[0]
	resp, body := doRawRequest(t, f.api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire", `{"idempotencyKey":"hs-seq-1","armToken":"`+token+`"}`, auth))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"shutdownSequences":["projectors"]`) {
		t.Fatalf("fire: %d %s, want projectors started and fm skipped", resp.StatusCode, body)
	}
	done := make(chan struct{})
	go func() { f.api.h.sitePowerRuns.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("hard stop waited on the sequence's hour-long wait")
	}
	// The immediate stop blacks out Resolume too, so count only what the sequences added.
	if sent := len(f.resolumeActionsSent()) - before; sent < 2 {
		t.Fatalf("resolume calls after hard stop = %d more, want at least the lamp step and the power cut", sent)
	}
}

func TestEmergencyStopRefusesASkipListOnLevelOne(t *testing.T) {
	f := newEmergencyStopFixture(t, testNow)
	auth := map[string]string{"Authorization": "Bearer " + f.adminToken}
	resp, body := doRawRequest(t, f.api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.emergencystop", `{"stop":{"actions":[],"skipSequences":["fm"]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`, auth))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Stop does not run shutdown sequences") {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

// Stop and power down with no night open runs its sequences from the
// emergency stop itself.
func TestStopPowerDownWithNoNightStartsItsSequences(t *testing.T) {
	f := newEmergencyStopFixture(t, testNow)
	mustPutAction(t, f.api, f.adminToken, "lamps-off", validShowActionResolumeBlackoutBody)
	if code, body := f.putSitePower(t, `{"shutdownSequences":[{"id":"fm","label":"FM transmitter","steps":[{"action":"lamps-off"}]}]}`); code != http.StatusOK {
		t.Fatalf("PUT show.sitepower: %d %s", code, body)
	}
	auth := map[string]string{"Authorization": "Bearer " + f.adminToken}
	resp, body := doRawRequest(t, f.api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop-power-down", `{"idempotencyKey":"spd-seq-1"}`, auth))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"shutdownSequences":["fm"]`) {
		t.Fatalf("stop-power-down: %d %s", resp.StatusCode, body)
	}
	f.api.h.sitePowerRuns.Wait()
}
