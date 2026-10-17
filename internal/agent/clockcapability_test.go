package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDetectClockPTPCapability(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"eno1", "eno2", "lo", "docker0"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, physical := range []string{"eno1", "eno2"} {
		if err := os.MkdirAll(filepath.Join(root, physical, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prevRoot, prevPHC, prevExt := sysClassNet, phcIndexForNetworkInterface, externalPTPSocketPresent
	sysClassNet = root
	phcIndexForNetworkInterface = func(name string) (int, bool, error) { return 0, name == "eno2", nil }
	externalPTPSocketPresent = func() bool { return true }
	t.Cleanup(func() {
		sysClassNet, phcIndexForNetworkInterface, externalPTPSocketPresent = prevRoot, prevPHC, prevExt
	})

	c, ok := detectClockPTPCapability()
	if !ok || c.ID != "clock.ptp" || c.Version != 1 {
		t.Fatalf("capability = %+v ok=%v, want clock.ptp v1", c, ok)
	}
	want := []any{map[string]any{"name": "eno1"}, map[string]any{"name": "eno2", "phcDevice": "/dev/ptp0"}}
	if !reflect.DeepEqual(c.Attributes["interfaces"], want) {
		t.Fatalf("interfaces = %#v, want %#v (physical interfaces only, with the PHC device)", c.Attributes["interfaces"], want)
	}
	if c.Attributes["externalProvider"] != true {
		t.Fatalf("externalProvider = %v, want true", c.Attributes["externalProvider"])
	}
}

func TestDetectClockPTPCapability_NoPhysicalInterface(t *testing.T) {
	prev := sysClassNet
	sysClassNet = t.TempDir()
	t.Cleanup(func() { sysClassNet = prev })
	if _, ok := detectClockPTPCapability(); ok {
		t.Fatalf("want no clock.ptp capability when no physical interface exists")
	}
}
