package api

import (
	"net/http"
	"sort"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/versions"
	"github.com/showmeshsystems/showmesh/internal/version"
)

// ReleaseStatusReader returns the cached newest-release answer without waiting on the network.
type ReleaseStatusReader interface {
	Status() versions.Status
}

const (
	nodeVersionNotReportedReason   = "This node has not reported a version yet."
	pluginVersionNotReportedReason = "The FPP plugin does not report its version."
)

// handleSystemVersions serves GET /api/v1/system/versions.
func (h *handlers) handleSystemVersions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	nodeViews, err := h.deps.Nodes.Snapshot(r.Context(), now)
	if err != nil {
		h.writeInternalError(w, now, "list nodes", err)
		return
	}
	declByNodeID, _, err := fetchDeclarationContext(r.Context(), h.deps.Discovery)
	if err != nil {
		h.writeInternalError(w, now, "fetch node declarations", err)
		return
	}
	nodeViews = mergeDeclaredOnlyNodes(nodeViews, declByNodeID)
	fppViews, err := h.deps.FPP.ListInstances(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances", err)
		return
	}

	nodes := make([]v1.SystemComponentVersion, 0, len(nodeViews))
	for _, nv := range nodeViews {
		c := v1.SystemComponentVersion{ID: nv.NodeID}
		if nv.Hello == nil || nv.Hello.AgentVersion == "" {
			c.Reason = strPtr(nodeVersionNotReportedReason)
		} else {
			c.Version = strPtr(nv.Hello.AgentVersion)
			if warning := versions.NodeWarning(nv.NodeID, nv.Hello.AgentVersion, version.Version); warning != "" {
				c.Mismatch, c.Warning = true, strPtr(warning)
			}
		}
		nodes = append(nodes, c)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	plugins := make([]v1.SystemComponentVersion, 0, len(fppViews))
	for _, fv := range fppViews {
		plugins = append(plugins, v1.SystemComponentVersion{ID: fv.InstanceID, Reason: strPtr(pluginVersionNotReportedReason)})
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].ID < plugins[j].ID })

	var status versions.Status
	if h.deps.Releases != nil {
		status = h.deps.Releases.Status()
	}
	jsonWrite(w, v1.SystemVersionsResponse{
		ServerTime:  formatTime(now),
		Coordinator: v1.SystemCoordinatorVersion{Version: version.Version},
		Nodes:       nodes,
		Plugins:     plugins,
		Release:     mapSystemRelease(status, version.Version),
	})
}

func mapSystemRelease(s versions.Status, current string) v1.SystemRelease {
	out := v1.SystemRelease{CheckEnabled: s.Enabled, CheckIntervalSeconds: int64(s.Interval / time.Second)}
	if s.Problem != "" {
		out.Problem = strPtr(s.Problem)
	}
	if s.Release == nil {
		return out
	}
	out.CheckedAt = strPtr(formatTime(s.CheckedAt))
	out.Tag = strPtr(s.Release.Tag)
	if !s.Release.PublishedAt.IsZero() {
		out.PublishedAt = strPtr(formatTime(s.Release.PublishedAt))
	}
	out.InstallCommand = strPtr(versions.InstallCommand(s.Release.Tag))
	out.Available = versions.Newer(s.Release.Tag, current)
	return out
}
