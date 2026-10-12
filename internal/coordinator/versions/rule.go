// Package versions holds the coordinator's view of what version every
// component runs: the compatibility rule and the newest-release check.
package versions

import (
	"strconv"
	"strings"
)

// semver is the part of a version string the rule and the release comparison read.
type semver struct {
	major, minor, patch int
	pre                 string
}

// parse reads "1.2.3", "v1.2.3" and "1.2.3-rc1+build"; ok is false for anything else, such as "dev".
func parse(raw string) (semver, bool) {
	s := normalize(raw)
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, true
}

func normalize(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "v")
}

// Compatible reports whether a component on version other interoperates with
// a coordinator on version coordinator. Before 1.0.0 nothing is promised, so
// only the same version passes; from 1.0.0 the same major passes.
func Compatible(coordinator, other string) bool {
	if normalize(coordinator) == normalize(other) {
		return true
	}
	c, ok := parse(coordinator)
	if !ok {
		return false
	}
	o, ok := parse(other)
	if !ok {
		return false
	}
	return c.major >= 1 && c.major == o.major
}

// Newer reports whether release is a later version than current. It is false
// when either cannot be read as a version, so a development build never claims an upgrade.
func Newer(release, current string) bool {
	r, ok := parse(release)
	if !ok {
		return false
	}
	c, ok := parse(current)
	if !ok {
		return false
	}
	for _, d := range []int{r.major - c.major, r.minor - c.minor, r.patch - c.patch} {
		if d != 0 {
			return d > 0
		}
	}
	return r.pre == "" && c.pre != ""
}

// InstallCommand is the one line that installs or upgrades to the release tagged tag.
func InstallCommand(tag string) string {
	return "curl -fsSL https://github.com/" + Repository + "/releases/download/" + tag + "/get-showmesh.sh | sudo bash"
}

// Repository is the GitHub repository whose releases the coordinator checks.
const Repository = "ShowMeshSystems/showmesh"

// NodeWarning is the operator's one line for a node whose version the rule
// does not accept, or "" when the node is compatible or has reported no version.
func NodeWarning(nodeID, nodeVersion, coordinator string) string {
	if nodeVersion == "" || Compatible(coordinator, nodeVersion) {
		return ""
	}
	return "Node " + nodeID + " runs " + normalize(nodeVersion) + ". Install " + normalize(coordinator) + " on it to match the coordinator."
}
