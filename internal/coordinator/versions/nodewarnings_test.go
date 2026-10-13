package versions

import (
	"reflect"
	"testing"
)

func TestNodeWarningsStatesEachVersionOnce(t *testing.T) {
	for _, tc := range []struct {
		name        string
		nodes       []NodeVersion
		coordinator string
		want        []string
	}{
		{"no nodes", nil, "0.2.0", []string{}},
		{"one node, published coordinator", []NodeVersion{{"a", "v0.1.5"}}, "0.2.0",
			[]string{"Node a runs 0.1.5. Install 0.2.0 on it to match the coordinator."}},
		{"two nodes share a version", []NodeVersion{{"a", "main-82a9da62"}, {"b", "main-82a9da62"}}, "main-8de657f6",
			[]string{"Nodes a and b run main-82a9da62. The coordinator runs main-8de657f6."}},
		{"many nodes are counted", []NodeVersion{{"a", "0.1.5"}, {"b", "0.1.5"}, {"c", "0.1.5"}}, "0.2.0",
			[]string{"3 nodes run 0.1.5. Install 0.2.0 on them to match the coordinator."}},
		{"two versions are two lines", []NodeVersion{{"a", "0.1.5"}, {"b", "0.1.4"}}, "0.2.0",
			[]string{"Node a runs 0.1.5. Install 0.2.0 on it to match the coordinator.", "Node b runs 0.1.4. Install 0.2.0 on it to match the coordinator."}},
	} {
		if got := NodeWarnings(tc.nodes, tc.coordinator); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
