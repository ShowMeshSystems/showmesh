package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/coordinator/versions"
	"github.com/showmeshsystems/showmesh/internal/version"
)

type releaseStatusFake struct{ status versions.Status }

func (f releaseStatusFake) Status() versions.Status       { return f.status }
func (f releaseStatusFake) SetSettings(versions.Settings) {}

func getSystemVersions(t *testing.T, releases ReleaseChecker) v1.SystemVersionsResponse {
	t.Helper()
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "0.2.0"
	deps := Dependencies{
		Nodes: &fakeNodeLister{views: []inventory.NodeView{
			{NodeID: "node-b", Hello: &store.HelloRecord{AgentVersion: "0.1.5"}},
			{NodeID: "node-a", Hello: &store.HelloRecord{AgentVersion: "0.2.0"}},
			{NodeID: "node-c"},
		}},
		FPP:      &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-1"}}},
		Releases: releases,
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/system/versions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var got v1.SystemVersionsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response: %v\n%s", err, body)
	}
	return got
}

func TestSystemVersionsListsEveryComponentAndWarnsOnAMismatch(t *testing.T) {
	got := getSystemVersions(t, nil)

	if got.Coordinator.Version != "0.2.0" {
		t.Fatalf("coordinator version = %q", got.Coordinator.Version)
	}
	if len(got.Nodes) != 3 || got.Nodes[0].ID != "node-a" || got.Nodes[1].ID != "node-b" || got.Nodes[2].ID != "node-c" {
		t.Fatalf("nodes = %+v, want node-a, node-b, node-c", got.Nodes)
	}
	if a := got.Nodes[0]; a.Mismatch || a.Warning != nil || a.Version == nil || *a.Version != "0.2.0" {
		t.Errorf("node-a = %+v, want 0.2.0 with no warning", a)
	}
	wantWarning := "Node node-b runs 0.1.5. Install 0.2.0 on it to match the coordinator."
	if b := got.Nodes[1]; !b.Mismatch || b.Warning == nil || *b.Warning != wantWarning {
		t.Errorf("node-b = %+v, want the mismatch warning", b)
	}
	if c := got.Nodes[2]; c.Version != nil || c.Mismatch || c.Reason == nil || *c.Reason != "This node has not reported a version yet." {
		t.Errorf("node-c = %+v, want a null version with its reason", c)
	}
	if len(got.Plugins) != 1 {
		t.Fatalf("plugins = %+v, want player-1", got.Plugins)
	}
	if p := got.Plugins[0]; p.ID != "player-1" || p.Version != nil || p.Mismatch || p.Reason == nil || *p.Reason != "The FPP plugin does not report its version." {
		t.Errorf("plugin = %+v, want a null version with its reason", p)
	}
}

func TestSystemVersionsWithoutACachedReleaseAnswer(t *testing.T) {
	got := getSystemVersions(t, nil).Release
	if got.CheckEnabled || got.Tag != nil || got.CheckedAt != nil || got.InstallCommand != nil || got.Available || got.Problem != nil {
		t.Fatalf("no checker: release = %+v, want everything empty", got)
	}

	problem := "GitHub could not be reached. The coordinator will try again."
	got = getSystemVersions(t, releaseStatusFake{versions.Status{Enabled: true, Interval: 6 * time.Hour, Problem: problem}}).Release
	if !got.CheckEnabled || got.CheckIntervalSeconds != 21600 || got.Tag != nil || got.Available {
		t.Fatalf("failed first check: release = %+v", got)
	}
	if got.Problem == nil || *got.Problem != problem {
		t.Fatalf("problem = %v, want %q", got.Problem, problem)
	}
}

func TestSystemVersionsWithACachedReleaseAnswer(t *testing.T) {
	published := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	checked := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	status := versions.Status{Enabled: true, Interval: 6 * time.Hour, CheckedAt: checked, Release: &versions.Release{Tag: "v0.3.0", PublishedAt: published}}
	got := getSystemVersions(t, releaseStatusFake{status}).Release

	if got.Tag == nil || *got.Tag != "v0.3.0" || !got.Available || got.Problem != nil {
		t.Fatalf("release = %+v, want v0.3.0 available", got)
	}
	if got.CheckedAt == nil || *got.CheckedAt != "2026-10-12T18:00:00Z" || got.PublishedAt == nil || *got.PublishedAt != "2026-10-10T12:00:00Z" {
		t.Fatalf("times = %v, %v", got.CheckedAt, got.PublishedAt)
	}
	want := "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.3.0/get-showmesh.sh | sudo bash -s -- --yes"
	if got.InstallCommand == nil || *got.InstallCommand != want {
		t.Fatalf("installCommand = %v, want %q", got.InstallCommand, want)
	}

	status.Release.Tag = "v0.2.0"
	if got := getSystemVersions(t, releaseStatusFake{status}).Release; got.Available {
		t.Fatalf("the coordinator's own version reads as an available upgrade: %+v", got)
	}
}
