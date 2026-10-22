package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const integrationBrokerSecret = "ha-pass-71c2"

// storeBrokerSource reads the stored brokers the way the coordinator's set does.
type storeBrokerSource struct {
	st       *store.Store
	builtIn  *config.IntegrationBroker
	notified int
}

func (s *storeBrokerSource) IntegrationBrokers(ctx context.Context) ([]config.IntegrationBroker, error) {
	var out []config.IntegrationBroker
	if s.builtIn != nil {
		out = append(out, *s.builtIn)
	}
	objs, err := s.st.ListConfigObjects(ctx, config.IntegrationBrokerConfigKind)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		out = append(out, config.IntegrationBroker{ID: o.ID})
	}
	return out, nil
}

func (s *storeBrokerSource) BuiltIn() (config.IntegrationBroker, bool) {
	if s.builtIn == nil {
		return config.IntegrationBroker{}, false
	}
	return *s.builtIn, true
}

func (s *storeBrokerSource) Notify() { s.notified++ }

var testBuiltInBroker = config.IntegrationBroker{
	ID: config.BuiltInIntegrationBrokerID, URL: "tcp://mosquitto:1883", Username: config.BuiltInIntegrationBrokerUsername,
	Password: "generated", TopicPrefix: config.BuiltInIntegrationTopicPrefix,
}

func integrationBrokerTestAPI(t *testing.T, envVarSet bool) (*API, *store.Store, *storeBrokerSource, string) {
	t.Helper()
	idSvc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "admin-1", identity.RoleAdmin).ID)
	src := &storeBrokerSource{st: st, builtIn: &testBuiltInBroker}
	deps := showConfigTestDeps(idSvc, st)
	deps.IntegrationBrokers = src
	deps.IntegrationBrokersEnvVarSet = envVarSet
	return New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()}), st, src, admin
}

func TestIntegrationBrokerPasswordIsNeverReturned(t *testing.T) {
	api, st, src, admin := integrationBrokerTestAPI(t, false)
	path := "/api/v1/config/integration.broker/home-assistant"
	resp, body := send(t, api, http.MethodPut, path, `{"url":"tcp://10.0.0.5:1883","username":"showmesh","password":"`+integrationBrokerSecret+`"}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", resp.StatusCode, body)
	}
	b := decodeMap(t, body)["broker"].(map[string]any)
	if b["url"] != "tcp://10.0.0.5:1883" || b["username"] != "showmesh" || b["passwordSet"] != true || b["builtIn"] != false {
		t.Fatalf("broker = %v", b)
	}
	if src.notified != 1 {
		t.Fatalf("source notified %d times, want 1", src.notified)
	}
	if v, ok, err := st.GetCredential(context.Background(), config.IntegrationBrokerConfigKind, "home-assistant", config.IntegrationBrokerPasswordCredentialField); err != nil || !ok || v != integrationBrokerSecret {
		t.Fatalf("stored password = %q, %v, %v", v, ok, err)
	}
	for _, target := range []string{path, path + "/revisions", "/api/v1/config/integration.broker"} {
		resp, got := doRequest(t, api.Handler, http.MethodGet, target, bearer(admin))
		if resp.StatusCode != http.StatusOK || strings.Contains(string(got), integrationBrokerSecret) || strings.Contains(string(got), "generated") {
			t.Fatalf("GET %s = %d, leaked a password or failed: %s", target, resp.StatusCode, got)
		}
	}

	resp, body = send(t, api, http.MethodPut, path, `{"username":"other"}`, admin)
	if b := decodeMap(t, body)["broker"].(map[string]any); resp.StatusCode != http.StatusOK || b["passwordSet"] != true || b["url"] != "tcp://10.0.0.5:1883" {
		t.Fatalf("partial update lost a field: %d %s", resp.StatusCode, body)
	}
	resp, body = send(t, api, http.MethodPut, path, `{"password":null}`, admin)
	if b := decodeMap(t, body)["broker"].(map[string]any); resp.StatusCode != http.StatusOK || b["passwordSet"] != false {
		t.Fatalf("clearing the password: %d %s", resp.StatusCode, body)
	}
	if _, ok, _ := st.GetCredential(context.Background(), config.IntegrationBrokerConfigKind, "home-assistant", config.IntegrationBrokerPasswordCredentialField); ok {
		t.Fatal("the password is still stored after it was cleared")
	}
}

func TestIntegrationBrokerListShowsTheBuiltInBrokerFirst(t *testing.T) {
	api, _, _, admin := integrationBrokerTestAPI(t, false)
	send(t, api, http.MethodPut, "/api/v1/config/integration.broker/aaa", `{"url":"tcp://10.0.0.5:1883"}`, admin)
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/integration.broker", bearer(admin))
	m := decodeMap(t, body)
	brokers := m["brokers"].([]any)
	if resp.StatusCode != http.StatusOK || len(brokers) != 2 || m["editsBlocked"] != "" {
		t.Fatalf("list = %d: %s", resp.StatusCode, body)
	}
	first := brokers[0].(map[string]any)
	if first["id"] != "built-in" || first["builtIn"] != true || first["topicPrefix"] != "showmesh-actions/" || first["passwordSet"] != true {
		t.Fatalf("first broker = %v", first)
	}
	if brokers[1].(map[string]any)["id"] != "aaa" {
		t.Fatalf("second broker = %v", brokers[1])
	}
	resp, body = doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/integration.broker/built-in", bearer(admin))
	if resp.StatusCode != http.StatusOK || decodeMap(t, body)["broker"].(map[string]any)["builtIn"] != true {
		t.Fatalf("GET built-in = %d: %s", resp.StatusCode, body)
	}
}

func TestIntegrationBrokerRefusesBadWrites(t *testing.T) {
	api, _, _, admin := integrationBrokerTestAPI(t, false)
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"built-in", "/api/v1/config/integration.broker/built-in", `{"url":"tcp://10.0.0.5:1883"}`, http.StatusConflict},
		{"bad id", "/api/v1/config/integration.broker/Bad_Id", `{"url":"tcp://10.0.0.5:1883"}`, http.StatusBadRequest},
		{"no url on create", "/api/v1/config/integration.broker/ha", `{"username":"x"}`, http.StatusBadRequest},
		{"login in url", "/api/v1/config/integration.broker/ha", `{"url":"tcp://u:p@10.0.0.5:1883"}`, http.StatusBadRequest},
		{"no scheme", "/api/v1/config/integration.broker/ha", `{"url":"10.0.0.5:1883"}`, http.StatusBadRequest},
		{"http scheme", "/api/v1/config/integration.broker/ha", `{"url":"http://10.0.0.5"}`, http.StatusBadRequest},
		{"unknown field", "/api/v1/config/integration.broker/ha", `{"url":"tcp://10.0.0.5:1883","topic":"x"}`, http.StatusBadRequest},
		{"null url", "/api/v1/config/integration.broker/ha", `{"url":null}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := send(t, api, http.MethodPut, tc.path, tc.body, admin)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}
	if resp, _ := send(t, api, http.MethodDelete, "/api/v1/config/integration.broker/built-in", `{"confirm":true}`, admin); resp.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE built-in = %d, want 409", resp.StatusCode)
	}
	if resp, _ := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/integration.broker/ha", bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a refused write created the broker: GET status = %d", resp.StatusCode)
	}
}

func TestIntegrationBrokerEditsAreRefusedWhileTheVariableIsSet(t *testing.T) {
	api, _, _, admin := integrationBrokerTestAPI(t, true)
	resp, body := send(t, api, http.MethodPut, "/api/v1/config/integration.broker/ha", `{"url":"tcp://10.0.0.5:1883"}`, admin)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "SHOWMESH_INTEGRATION_BROKERS") {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, api, http.MethodDelete, "/api/v1/config/integration.broker/ha", `{"confirm":true}`, admin); resp.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE = %d, want 409", resp.StatusCode)
	}
	_, body = doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/integration.broker", bearer(admin))
	if decodeMap(t, body)["editsBlocked"] == "" {
		t.Fatalf("list does not say edits are blocked: %s", body)
	}
}

func mqttActionBody(broker, topic, expectTopic string) string {
	expect := `{"kind":"none"}`
	if expectTopic != "" {
		expect = `{"kind":"text","topic":"` + expectTopic + `","deadlineSeconds":10}`
	}
	return `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"mqtt","broker":"` + broker + `",
		"publish":{"topic":"` + topic + `","payload":"ON","qos":1},"expect":` + expect + `}}`
}

func TestMQTTActionOnTheBuiltInBrokerMustUseItsTopicPrefix(t *testing.T) {
	api, _, _, admin := integrationBrokerTestAPI(t, false)
	mustPutShow(t, api, admin, "halloween-2026", `{"name":"halloween-2026"}`)
	cases := []struct {
		name, body string
		want       int
	}{
		{"inside the prefix", mqttActionBody("built-in", "showmesh-actions/projectors/set", "showmesh-actions/projectors/state"), http.StatusOK},
		{"publish outside", mqttActionBody("built-in", "showmesh/nodes/node-01/cmd", ""), http.StatusBadRequest},
		{"fpp command", mqttActionBody("built-in", "falcon/player/fpp/set/command", ""), http.StatusBadRequest},
		{"prefix alone", mqttActionBody("built-in", "showmesh-actions/", ""), http.StatusBadRequest},
		{"expect outside", mqttActionBody("built-in", "showmesh-actions/projectors/set", "home/projectors/state"), http.StatusBadRequest},
		{"unknown broker", mqttActionBody("nope", "home/projectors/set", ""), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := send(t, api, http.MethodPut, "/api/v1/config/show.action/a1", tc.body, admin)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}
}

func TestIntegrationBrokerDeleteIsRefusedWhileAnActionUsesIt(t *testing.T) {
	api, st, _, admin := integrationBrokerTestAPI(t, false)
	mustPutShow(t, api, admin, "halloween-2026", `{"name":"halloween-2026"}`)
	path := "/api/v1/config/integration.broker/ha"
	send(t, api, http.MethodPut, path, `{"url":"tcp://10.0.0.5:1883","password":"`+integrationBrokerSecret+`"}`, admin)
	if resp, body := send(t, api, http.MethodPut, "/api/v1/config/show.action/projectors-on", mqttActionBody("ha", "home/projectors/set", ""), admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT action = %d: %s", resp.StatusCode, body)
	}

	resp, body := send(t, api, http.MethodDelete, path, `{"confirm":true}`, admin)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "projectors-on") {
		t.Fatalf("DELETE in use = %d: %s", resp.StatusCode, body)
	}
	if resp, body := send(t, api, http.MethodDelete, "/api/v1/config/show.action/projectors-on", `{"confirm":true}`, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE action = %d: %s", resp.StatusCode, body)
	}
	if resp, body := send(t, api, http.MethodDelete, path, `{"confirm":true}`, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE unused = %d: %s", resp.StatusCode, body)
	}
	if _, ok, _ := st.GetCredential(context.Background(), config.IntegrationBrokerConfigKind, "ha", config.IntegrationBrokerPasswordCredentialField); ok {
		t.Fatal("the password is still stored after the broker was removed")
	}
}

func TestOpenAPIIntegrationBrokerResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, _, _, admin := integrationBrokerTestAPI(t, false)
	path := "/api/v1/config/integration.broker/ha"
	body := `{"url":"tcp://10.0.0.5:1883","username":"showmesh","password":"x"}`
	assertMatchesSchema(t, c, "IntegrationBrokerPutRequest", []byte(body))
	resp, got := send(t, api, http.MethodPut, path, body, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, got)
	}
	assertMatchesSchema(t, c, "IntegrationBrokerResponse", got)
	_, got = doRequest(t, api.Handler, http.MethodGet, path, bearer(admin))
	assertMatchesSchema(t, c, "IntegrationBrokerResponse", got)
	_, got = doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/integration.broker/built-in", bearer(admin))
	assertMatchesSchema(t, c, "IntegrationBrokerResponse", got)
	_, got = doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/integration.broker", bearer(admin))
	assertMatchesSchema(t, c, "IntegrationBrokersListResponse", got)
	_, got = doRequest(t, api.Handler, http.MethodGet, path+"/revisions", bearer(admin))
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", got)
}
