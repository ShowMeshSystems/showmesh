// Package versions holds the coordinator's view of what version every
// component runs: the compatibility rule and the newest-release check.
package versions

import (
	"regexp"
	"strconv"
	"strings"
)

// semver is the part of a version string the rule and the release comparison read.
type semver struct {
	major, minor, patch int
	pre                 string
	// ahead is true for a build made from commits after its tag, as git describe names it.
	ahead bool
}

// describeSuffix matches what git describe appends to a tag: "3-gabc1234", "dirty", or both.
var describeSuffix = regexp.MustCompile(`^(?:(.+?)-)??(?:\d+-g[0-9a-f]+(?:-dirty)?|dirty)$`)

// parse reads "1.2.3", "v1.2.3", "1.2.3-rc1+build" and "1.2.3-3-gabc1234"; ok is false for anything else, such as "dev".
func parse(raw string) (semver, bool) {
	s := normalize(raw)
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
		if m := describeSuffix.FindStringSubmatch(v.pre); m != nil {
			v.pre, v.ahead = m[1], true
		}
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

// compare orders two versions: negative when a is earlier, positive when a is
// later. A prerelease sorts before its release, and a build ahead of a tag after it.
func compare(a, b semver) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch, a.rank() - b.rank()} {
		if d != 0 {
			return d
		}
	}
	return strings.Compare(a.pre, b.pre)
}

func (v semver) rank() int {
	switch {
	case v.pre != "":
		return -1
	case v.ahead:
		return 1
	}
	return 0
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
	return compare(r, c) > 0
}

// InstallCommand is the one line that installs or upgrades to the release
// tagged tag. It matches what the installer prints for a node upgrade.
func InstallCommand(tag string) string {
	return "curl -fsSL https://github.com/" + Repository + "/releases/download/" + tag + "/get-showmesh.sh | sudo bash -s -- --yes"
}

// Repository is the GitHub repository whose releases the coordinator checks.
const Repository = "ShowMeshSystems/showmesh"

// NodeWarning is the operator's one line for a node whose version the rule
// does not accept, or "" when the node is compatible or has reported no version.
// It names a version to install only when the coordinator runs a published one.
func NodeWarning(nodeID, nodeVersion, coordinator string) string {
	if nodeVersion == "" || Compatible(coordinator, nodeVersion) {
		return ""
	}
	fact := "Node " + nodeID + " runs " + normalize(nodeVersion) + "."
	if c, ok := parse(coordinator); !ok || c.ahead {
		return fact + " The coordinator runs " + normalize(coordinator) + "."
	}
	return fact + " Install " + normalize(coordinator) + " on it to match the coordinator."
}

// NodeVersion is one node and the version it last reported.
type NodeVersion struct {
	NodeID, Version string
}

// NodeWarnings is the playlist readiness form of [NodeWarning]: one line per
// version, naming every node on it, so a fleet one release behind reads as
// one message. nodes must already be the ones the rule does not accept.
func NodeWarnings(nodes []NodeVersion, coordinator string) []string {
	byVersion := map[string][]string{}
	var order []string
	for _, n := range nodes {
		v := normalize(n.Version)
		if _, seen := byVersion[v]; !seen {
			order = append(order, v)
		}
		byVersion[v] = append(byVersion[v], n.NodeID)
	}
	c, ok := parse(coordinator)
	published := ok && !c.ahead
	warnings := make([]string, 0, len(order))
	for _, v := range order {
		ids := byVersion[v]
		subject, verb, target := "Node "+ids[0], "runs", "it"
		switch {
		case len(ids) == 2:
			subject, verb, target = "Nodes "+ids[0]+" and "+ids[1], "run", "them"
		case len(ids) > 2:
			subject, verb, target = strconv.Itoa(len(ids))+" nodes", "run", "them"
		}
		fact := subject + " " + verb + " " + v + "."
		if published {
			warnings = append(warnings, fact+" Install "+normalize(coordinator)+" on "+target+" to match the coordinator.")
			continue
		}
		warnings = append(warnings, fact+" The coordinator runs "+normalize(coordinator)+".")
	}
	return warnings
}
