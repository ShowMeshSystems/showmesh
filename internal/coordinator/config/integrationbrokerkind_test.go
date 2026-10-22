package config

import "testing"

func TestIntegrationTopicProblem(t *testing.T) {
	builtIn := IntegrationBroker{ID: BuiltInIntegrationBrokerID, TopicPrefix: BuiltInIntegrationTopicPrefix}
	open := IntegrationBroker{ID: "ha"}
	cases := []struct {
		broker IntegrationBroker
		topic  string
		ok     bool
	}{
		{builtIn, "showmesh-actions/projectors/set", true},
		{builtIn, "showmesh-actions/", false},
		{builtIn, "showmesh-actionsx/a", false},
		{builtIn, "showmesh/nodes/node-01/cmd", false},
		{builtIn, "falcon/player/fpp/set/command", false},
		{open, "anything/at/all", true},
	}
	for _, tc := range cases {
		if got := IntegrationTopicProblem(tc.broker, tc.topic) == ""; got != tc.ok {
			t.Errorf("%s on %s: allowed = %v, want %v", tc.topic, tc.broker.ID, got, tc.ok)
		}
	}
}

func TestIntegrationBrokerURLAndIDProblems(t *testing.T) {
	for _, u := range []string{"tcp://10.0.0.5:1883", "mqtts://broker.lan:8883", "ws://broker:9001/mqtt"} {
		if msg := IntegrationBrokerURLProblem(u); msg != "" {
			t.Errorf("%s refused: %s", u, msg)
		}
	}
	for _, u := range []string{"", "10.0.0.5:1883", "http://10.0.0.5", "tcp://user:pw@10.0.0.5:1883", "tcp://"} {
		if IntegrationBrokerURLProblem(u) == "" {
			t.Errorf("%q accepted", u)
		}
	}
	for id, ok := range map[string]bool{"ha": true, "home-assistant": true, "built-in": false, "Bad_Id": false, "": false, "-x": false} {
		if got := IntegrationBrokerIDProblem(id) == ""; got != ok {
			t.Errorf("id %q allowed = %v, want %v", id, got, ok)
		}
	}
}
