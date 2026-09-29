package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// nodeViewReportingOutputs builds the hello a current agent sends, decoded
// from JSON exactly as inventory holds it, so attribute types match the wire.
func nodeViewReportingOutputs(t *testing.T, nodeID string, live bool, localAttrsJSON string, ltcRoutes ...string) inventory.NodeView {
	t.Helper()
	var attrs map[string]any
	if err := json.Unmarshal([]byte(localAttrsJSON), &attrs); err != nil {
		t.Fatalf("attrs JSON: %v", err)
	}
	caps := capability.Set{{ID: "audio.output.local", Version: 1, Attributes: attrs}}
	if len(ltcRoutes) > 0 {
		routes := make([]any, len(ltcRoutes))
		for i, r := range ltcRoutes {
			routes[i] = r
		}
		caps = append(caps, capability.Capability{ID: "audio.output.ltc", Version: 1, Attributes: map[string]any{"routes": routes}})
	}
	nv := inventory.NodeView{NodeID: nodeID, Hello: &store.HelloRecord{Capabilities: caps}}
	if live {
		nv.Liveness = inventory.LivenessOnline
	}
	return nv
}

const m4OutputsAttrs = `{"routes":["alsa_output.m4"],"outputCount":1,"discoveryComplete":true,
 "outputs":[{"route":"alsa_output.m4","interface":"alsa_output.m4","source":"pipewire","channels":4,"channelBasis":"inventory","ltcCapable":true}]}`

const pchOutputsAttrs = `{"routes":["hw:CARD=PCH,DEV=0"],"outputCount":1,"discoveryComplete":true,
 "outputs":[{"route":"hw:CARD=PCH,DEV=0","interface":"hw:CARD=PCH","source":"alsa","channels":2,"channelBasis":"atLeast","ltcCapable":false}]}`

func newRoutingChoicesAPI(t *testing.T, views ...inventory.NodeView) (*API, string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews(views)
	return New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()}), token
}

func getRoutingChoices(t *testing.T, api *API, token, nodeID string) (int, map[string]any) {
	t.Helper()
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/"+nodeID+"/audio/routing-choices", map[string]string{"Authorization": "Bearer " + token})
	return resp.StatusCode, decodeMap(t, body)
}

func TestAudioRoutingChoicesAreIndependentPerNode(t *testing.T) {
	api, token := newRoutingChoicesAPI(t,
		nodeViewReportingOutputs(t, "audio-m4", true, m4OutputsAttrs, "alsa_output.m4"),
		nodeViewReportingOutputs(t, "audio-pi", true, pchOutputsAttrs),
	)

	status, m4 := getRoutingChoices(t, api, token, "audio-m4")
	if status != http.StatusOK || m4["discovery"] != "available" {
		t.Fatalf("audio-m4: status %d body %v, want 200 available", status, m4)
	}
	m4Routes := m4["routes"].([]any)
	if len(m4Routes) != 1 || m4Routes[0].(map[string]any)["route"] != "alsa_output.m4" || m4["ltc"].(map[string]any)["available"] != true {
		t.Errorf("audio-m4 routes %v ltc %v, want only its own 4-channel output with LTC", m4Routes, m4["ltc"])
	}

	_, pi := getRoutingChoices(t, api, token, "audio-pi")
	piRoutes := pi["routes"].([]any)
	if len(piRoutes) != 1 || piRoutes[0].(map[string]any)["route"] != "hw:CARD=PCH,DEV=0" || pi["ltc"].(map[string]any)["available"] != false {
		t.Errorf("audio-pi routes %v ltc %v, want only its own stereo output, program only", piRoutes, pi["ltc"])
	}
}

func TestAudioRoutingChoicesOldAgentKeepsManualPathAndConfig(t *testing.T) {
	old := nodeViewWithAudioCapabilities("audio-old", []string{"hw:0,0"}, []string{"hw:0,0"})
	old.Liveness = inventory.LivenessOnline
	api, token := newRoutingChoicesAPI(t, old)

	if status, body := mustPutAudioNode(t, api, token, "audio-old", validAudioNodeBody); status != http.StatusOK {
		t.Fatalf("PUT against an old agent: %d %s, want 200 (manual path)", status, body)
	}
	_, got := getRoutingChoices(t, api, token, "audio-old")
	if got["discovery"] != "not_reported" || got["manualEntry"].(map[string]any)["allowed"] != true || len(got["routes"].([]any)) != 0 {
		t.Errorf("old agent choices = %v, want not_reported, manual allowed, no routes", got)
	}
	cur, _ := got["current"].(map[string]any)
	if cur["programRoute"] != "hw:0,0" || cur["ltcChannel"] != float64(3) {
		t.Errorf("current = %v, want the stored placement reported unchanged", cur)
	}
}

func TestAudioRoutingChoicesStaleAndAbsentOfferNothing(t *testing.T) {
	api, token := newRoutingChoicesAPI(t, nodeViewReportingOutputs(t, "audio-off", false, m4OutputsAttrs))
	_, stale := getRoutingChoices(t, api, token, "audio-off")
	if stale["discovery"] != "stale" || len(stale["routes"].([]any)) != 0 || stale["reason"] == "" {
		t.Errorf("offline node = %v, want stale with no routes and a reason", stale)
	}
	_, absent := getRoutingChoices(t, api, token, "audio-none")
	if absent["discovery"] != "absent" || absent["manualEntry"].(map[string]any)["allowed"] != false {
		t.Errorf("unknown node = %v, want absent and manual entry refused", absent)
	}
}

func TestAudioRoutingChoicesMalformedOutputsFail(t *testing.T) {
	api, token := newRoutingChoicesAPI(t, nodeViewReportingOutputs(t, "audio-bad", true,
		`{"routes":["hw:0,0"],"discoveryComplete":true,"outputs":[{"route":"hw:0,0","channels":"four"}]}`))
	_, got := getRoutingChoices(t, api, token, "audio-bad")
	if got["discovery"] != "failed" || len(got["routes"].([]any)) != 0 {
		t.Errorf("malformed outputs = %v, want failed with no routes", got)
	}
}

func TestPutAudioNodeRefusesChannelBeyondReportedInventory(t *testing.T) {
	api, token := newRoutingChoicesAPI(t, nodeViewReportingOutputs(t, "audio-m4", true, m4OutputsAttrs, "alsa_output.m4"))
	status, body := mustPutAudioNode(t, api, token, "audio-m4",
		`{"programRoute":"alsa_output.m4","ltcRoute":"alsa_output.m4","programChannels":[1,2],"ltcChannel":5}`)
	if status != http.StatusBadRequest {
		t.Fatalf("channel 5 of 4: %d %s, want 400", status, body)
	}
	if status, body := mustPutAudioNode(t, api, token, "audio-m4",
		`{"programRoute":"alsa_output.m4","ltcRoute":"alsa_output.m4","programChannels":[1,2],"ltcChannel":3}`); status != http.StatusOK {
		t.Fatalf("offered choice refused: %d %s, want 200", status, body)
	}
	_, got := getRoutingChoices(t, api, token, "audio-m4")
	if cur := got["current"].(map[string]any); cur["offered"] != true {
		t.Errorf("current = %v, want the saved choice reported as offered", cur)
	}
	if clock := got["clock"].(map[string]any); clock["verification"] != "same_interface" || clock["localClock"] != "alsa_output.m4" {
		t.Errorf("clock = %v, want same_interface on alsa_output.m4", clock)
	}
}
