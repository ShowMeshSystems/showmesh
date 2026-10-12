package versions

import "testing"

func TestCompatibleBeforeOneZeroAcceptsOnlyTheSameVersion(t *testing.T) {
	for _, tc := range []struct {
		coordinator, other string
		want               bool
	}{
		{"0.2.0", "0.2.0", true},
		{"0.2.0", "v0.2.0", true},
		{"0.2.0", "0.2.1", false},
		{"0.2.0", "0.1.9", false},
		{"0.2.0", "1.0.0", false},
		{"0.2.0", "", false},
		{"dev", "dev", true},
		{"dev", "0.2.0", false},
		{"0.2.0", "dev", false},
	} {
		if got := Compatible(tc.coordinator, tc.other); got != tc.want {
			t.Errorf("Compatible(%q, %q) = %v, want %v", tc.coordinator, tc.other, got, tc.want)
		}
	}
}

func TestCompatibleFromOneZeroAcceptsTheSameMajor(t *testing.T) {
	for _, tc := range []struct {
		coordinator, other string
		want               bool
	}{
		{"1.0.0", "1.0.0", true},
		{"1.0.0", "1.4.2", true},
		{"1.4.2", "v1.0.0", true},
		{"1.2.0", "1.3.0-rc1", true},
		{"1.0.0", "2.0.0", false},
		{"2.1.0", "1.9.9", false},
		{"1.0.0", "0.9.0", false},
		{"1.0.0", "dev", false},
		{"1.0.0", "", false},
	} {
		if got := Compatible(tc.coordinator, tc.other); got != tc.want {
			t.Errorf("Compatible(%q, %q) = %v, want %v", tc.coordinator, tc.other, got, tc.want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		release, current string
		want             bool
	}{
		{"v0.2.0", "0.1.5", true},
		{"v0.2.0", "0.2.0", false},
		{"v0.1.9", "0.2.0", false},
		{"v1.0.0", "0.9.9", true},
		{"v0.2.0", "0.2.0-rc1", true},
		{"v0.2.0-rc1", "0.2.0", false},
		{"v0.2.0", "dev", false},
		{"v0.2.0", "0.2.0-3-gabc1234", false},
		{"v0.2.0", "0.2.0-3-gabc1234-dirty", false},
		{"v0.2.0", "0.2.0-dirty", false},
		{"v0.2.0", "0.2.0-rc.1-dirty", true},
		{"v0.2.1", "0.2.0-3-gabc1234", true},
		{"v0.2.0", "0.2.0-rc1-3-gabc1234", true},
		{"v0.2.0-rc2", "0.2.0-rc1", true},
		{"nightly", "0.2.0", false},
	} {
		if got := Newer(tc.release, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.release, tc.current, got, tc.want)
		}
	}
}

func TestInstallCommandNamesTheTaggedRelease(t *testing.T) {
	want := "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.2.0/get-showmesh.sh | sudo bash -s -- --yes"
	if got := InstallCommand("v0.2.0"); got != want {
		t.Errorf("InstallCommand = %q, want %q", got, want)
	}
}

func TestNodeWarningNamesBothVersionsOnlyOnAMismatch(t *testing.T) {
	want := "Node node-1 runs 0.1.5. Install 0.2.0 on it to match the coordinator."
	if got := NodeWarning("node-1", "v0.1.5", "0.2.0"); got != want {
		t.Errorf("NodeWarning = %q, want %q", got, want)
	}
	if got := NodeWarning("node-1", "0.2.0", "0.2.0"); got != "" {
		t.Errorf("matching versions warned: %q", got)
	}
	if got := NodeWarning("node-1", "", "0.2.0"); got != "" {
		t.Errorf("a node that reported no version warned: %q", got)
	}
}

func TestNodeWarningNamesNoInstallWhenTheCoordinatorRunsNoPublishedVersion(t *testing.T) {
	for coordinator, want := range map[string]string{
		"dev":              "Node node-1 runs 0.2.0. The coordinator runs dev.",
		"0.2.0-3-gabc1234": "Node node-1 runs 0.2.0. The coordinator runs 0.2.0-3-gabc1234.",
	} {
		if got := NodeWarning("node-1", "0.2.0", coordinator); got != want {
			t.Errorf("NodeWarning with coordinator %q = %q, want %q", coordinator, got, want)
		}
	}
}
