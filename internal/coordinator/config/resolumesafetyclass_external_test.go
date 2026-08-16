package config_test

// This file is deliberately package config_test (external), not package
// config, for the identical reason showaction_broker_deadline_test.go's own
// top comment gives: it is the one place that may import both
// internal/coordinator/config and internal/coordinator/collector/resolume
// in the same compilation unit, so it can prove
// config.ResolumeActionDeclaredSafetyClass agrees with that package's own
// registered safety class without either production package importing the
// other (TRACK-D-SEAM-C-MACRO-SPEC.md section 2.2: "write a test that reads
// both and fails if they diverge, rather than maintaining the table
// twice").

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestResolumeActionSafetyClassMatchesResolumeRegistry walks every action
// resolume.actionRegistry declares (via a zero-value *resolume.Collector —
// [resolume.ActionDispatcher.Actions] never touches its collector, it only
// copies that package-level registry) and checks config's own declared
// safety class agrees.
//
// Broken and confirmed to fail: temporarily changed
// resolumeActionDeclaredSafetyClass's own clearLayer entry from
// config.ShowSafetyClassBlackout to config.ShowSafetyClassNone in
// showaction.go — this test failed naming the divergence. Restored
// afterward.
func TestResolumeActionSafetyClassMatchesResolumeRegistry(t *testing.T) {
	d := resolume.NewActionDispatcher(&resolume.Collector{}, resolume.ActionDispatcherOptions{})
	descriptors := d.Actions()
	if len(descriptors) == 0 {
		t.Fatal("resolume.ActionDispatcher.Actions() returned nothing to compare against")
	}
	for _, desc := range descriptors {
		var want string
		switch desc.SafetyClass {
		case resolume.ActionSafetyClassExempt:
			want = config.ShowSafetyClassBlackout
		case resolume.ActionSafetyClassNotExempt:
			want = config.ShowSafetyClassNone
		default:
			t.Fatalf("action %q has an undeclared resolume.ActionSafetyClass %d", desc.Name, desc.SafetyClass)
		}
		got, ok := config.ResolumeActionDeclaredSafetyClass(string(desc.Name))
		if !ok {
			t.Fatalf("config.resolumeActionDeclaredSafetyClass has no entry for action %q, which resolume.actionRegistry declares", desc.Name)
		}
		if got != want {
			t.Fatalf("action %q: config declares safety class %q, resolume's own registry declares %v (-> %q); these must agree",
				desc.Name, got, desc.SafetyClass, want)
		}
	}
}
