package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/preview"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// previewSettingsSource loads the stored preview configuration, the credentials
// that go with it, and the surfaces and nodes used to label NDI tiles.
type previewSettingsSource struct {
	st    *store.Store
	nodes func(ctx context.Context) ([]inventory.NodeView, error)
}

func (p previewSettingsSource) activePayload(ctx context.Context, kind, id string) (string, bool, error) {
	obj, err := p.st.GetConfigObject(ctx, kind, id)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	rev, err := p.st.GetConfigRevision(ctx, kind, id, obj.CurrentRevision)
	if err != nil {
		return "", false, err
	}
	return rev.PayloadJSON, true, nil
}

func (p previewSettingsSource) Load(ctx context.Context) (preview.Settings, error) {
	var out preview.Settings

	if raw, ok, err := p.activePayload(ctx, config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID); err != nil {
		return out, fmt.Errorf("read preview.gateway: %w", err)
	} else if ok {
		cfg, _, err := config.DecodePreviewGatewayPayload(raw)
		if err != nil {
			return out, err
		}
		out.GatewayURL = cfg.URL
		out.Token, _, err = p.st.GetCredential(ctx, config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID, config.PreviewGatewayTokenCredentialField)
		if err != nil {
			return out, fmt.Errorf("read preview.gateway token: %w", err)
		}
	}

	if raw, ok, err := p.activePayload(ctx, config.DashboardLayoutConfigKind, config.DashboardLayoutConfigObjectID); err != nil {
		return out, fmt.Errorf("read dashboard.layout: %w", err)
	} else if ok {
		layout, err := config.DecodeDashboardLayoutPayload(raw)
		if err != nil {
			return out, err
		}
		out.Layout = preview.Layout{Order: layout.Previews.Order, Hidden: layout.Previews.Hidden}
	}

	cams, err := p.st.ListConfigObjects(ctx, config.PreviewCameraConfigKind)
	if err != nil {
		return out, fmt.Errorf("list preview.camera: %w", err)
	}
	for _, obj := range cams {
		raw, ok, err := p.activePayload(ctx, config.PreviewCameraConfigKind, obj.ID)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		payload, err := config.DecodePreviewCameraPayload(raw)
		if err != nil {
			return out, err
		}
		streamURL, _, err := p.st.GetCredential(ctx, config.PreviewCameraConfigKind, obj.ID, config.PreviewCameraURLCredentialField)
		if err != nil {
			return out, fmt.Errorf("read preview.camera %q url: %w", obj.ID, err)
		}
		out.Cameras = append(out.Cameras, preview.Camera{ID: obj.ID, Name: payload.Name, Enabled: payload.Enabled, URL: streamURL})
	}

	if out.GatewayURL == "" {
		return out, nil
	}
	surfaces, err := p.st.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return out, fmt.Errorf("list show.surface: %w", err)
	}
	for _, obj := range surfaces {
		raw, ok, err := p.activePayload(ctx, config.ShowSurfaceConfigKind, obj.ID)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		var sp config.ShowSurfacePayload
		if err := json.Unmarshal([]byte(raw), &sp); err != nil {
			return out, fmt.Errorf("decode show.surface %q: %w", obj.ID, err)
		}
		if sp.Output.NDI != nil {
			out.Surfaces = append(out.Surfaces, preview.Surface{NodeID: sp.Node, SurfaceID: obj.ID, SourceName: sp.Output.NDI.SourceName})
		}
	}
	if p.nodes != nil {
		views, err := p.nodes(ctx)
		if err != nil {
			return out, fmt.Errorf("list nodes: %w", err)
		}
		for _, v := range views {
			n := preview.Node{ID: v.NodeID}
			if v.Hello != nil && v.Hello.Label != "" {
				n.Names = append(n.Names, v.Hello.Label)
			}
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out, nil
}
