package coordinator

import (
	"context"
	"errors"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/preview"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func putPreviewRevision(t *testing.T, st *store.Store, kind, id, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{Kind: kind, ObjectID: id, Revision: 1, PayloadJSON: payload, Source: "api"}); err != nil {
		t.Fatalf("create %s/%s: %v", kind, id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, 1); err != nil {
		t.Fatalf("activate %s/%s: %v", kind, id, err)
	}
}

func TestPreviewSettingsSourceLoadsConfigurationCredentialsAndSurfaces(t *testing.T) {
	st, _ := newTestAssetSettingsSyncDeps(t)
	ctx := context.Background()
	src := previewSettingsSource{st: st, nodes: func(context.Context) ([]inventory.NodeView, error) {
		return []inventory.NodeView{{NodeID: "node-01", Hello: &store.HelloRecord{Label: "Showhost1"}}}, nil
	}}

	if got, err := src.Load(ctx); err != nil || got.GatewayURL != "" || len(got.Cameras) != 0 {
		t.Fatalf("empty store: %+v, %v", got, err)
	}

	putPreviewRevision(t, st, config.PreviewGatewayConfigKind, "default", `{"url":"http://gw:8095","tokenSet":true}`)
	if err := st.SetCredential(ctx, config.PreviewGatewayConfigKind, "default", "token", "tok"); err != nil {
		t.Fatal(err)
	}
	putPreviewRevision(t, st, config.PreviewCameraConfigKind, "driveway", `{"name":"Driveway","enabled":true,"urlSet":true,"urlDisplay":"rtsp://h:1"}`)
	if err := st.SetCredential(ctx, config.PreviewCameraConfigKind, "driveway", "url", "rtsp://h:1/a"); err != nil {
		t.Fatal(err)
	}
	putPreviewRevision(t, st, config.PreviewNDIConfigKind, "arena-far", `{"label":"Arena far","ndiName":"ARENA-02 (Composition)","address":"10.0.20.15:5961","enabled":true}`)
	putPreviewRevision(t, st, config.DashboardLayoutConfigKind, "default", `{"previews":{"order":["camera:driveway"],"hidden":[]}}`)
	putPreviewRevision(t, st, config.ShowSurfaceConfigKind, "matrix-main", `{"show":"s","name":"Matrix","node":"node-01","channelRange":{"startChannel":1,"channelCount":12},"geometry":{"width":2,"height":2,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"ndi","ndi":{"sourceName":"matrix-main"}}}`)
	putPreviewRevision(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, `{"show":"s"}`)
	putPreviewRevision(t, st, config.ShowSurfaceConfigKind, "matrix-old", `{"show":"other","name":"Old","node":"node-01","channelRange":{"startChannel":1,"channelCount":12},"geometry":{"width":2,"height":2,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"ndi","ndi":{"sourceName":"matrix-main"}}}`)
	putPreviewRevision(t, st, config.ShowSurfaceConfigKind, "hdmi-wall", `{"show":"s","name":"Wall","node":"node-02","channelRange":{"startChannel":1,"channelCount":12},"geometry":{"width":2,"height":2,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"hdmi","hdmi":{"display":"HDMI-1"}}}`)

	got, err := src.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayURL != "http://gw:8095" || got.Token != "tok" {
		t.Fatalf("gateway = %q token %q", got.GatewayURL, got.Token)
	}
	if len(got.Cameras) != 1 || got.Cameras[0].URL != "rtsp://h:1/a" || got.Cameras[0].Name != "Driveway" || !got.Cameras[0].Enabled {
		t.Fatalf("cameras = %+v", got.Cameras)
	}
	if want := (preview.NDISource{ID: "arena-far", Label: "Arena far", Name: "ARENA-02 (Composition)", Address: "10.0.20.15:5961", Enabled: true}); len(got.NDISources) != 1 || got.NDISources[0] != want {
		t.Fatalf("NDI sources = %+v", got.NDISources)
	}
	if len(got.Layout.Order) != 1 || got.Layout.Order[0] != "camera:driveway" {
		t.Fatalf("layout = %+v", got.Layout)
	}
	if len(got.Surfaces) != 1 || got.Surfaces[0].NodeID != "node-01" || got.Surfaces[0].SurfaceID != "matrix-main" || got.Surfaces[0].SourceName != "matrix-main" {
		t.Fatalf("surfaces = %+v (only NDI surfaces of the active show label tiles)", got.Surfaces)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Names[0] != "Showhost1" {
		t.Fatalf("nodes = %+v", got.Nodes)
	}
}

func TestPreviewSettingsLabelInputsAreBestEffort(t *testing.T) {
	st, _ := newTestAssetSettingsSyncDeps(t)
	ctx := context.Background()
	src := previewSettingsSource{st: st, nodes: func(context.Context) ([]inventory.NodeView, error) {
		return nil, errors.New("inventory unavailable")
	}}
	putPreviewRevision(t, st, config.PreviewGatewayConfigKind, "default", `{"url":"http://gw:8095","tokenSet":false}`)
	putPreviewRevision(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, `{"show":"s"}`)
	putPreviewRevision(t, st, config.ShowSurfaceConfigKind, "broken", `{not json`)
	putPreviewRevision(t, st, config.ShowSurfaceConfigKind, "matrix-main", `{"show":"s","name":"Matrix","node":"node-01","channelRange":{"startChannel":1,"channelCount":12},"geometry":{"width":2,"height":2,"pixelFormat":"rgb"},"frameRate":40,"output":{"transport":"ndi","ndi":{"sourceName":"matrix-main"}}}`)

	got, err := src.Load(ctx)
	if err != nil {
		t.Fatalf("a label input failure must not fail the load: %v", err)
	}
	if got.GatewayURL != "http://gw:8095" || len(got.Surfaces) != 1 || len(got.Nodes) != 0 {
		t.Fatalf("settings = %+v", got)
	}
}
