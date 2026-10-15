package config

import (
	"encoding/json"
	"fmt"
)

const (
	// DashboardLayoutConfigKind is the stored kind behind /api/v1/config/dashboard.layout.
	DashboardLayoutConfigKind = "dashboard.layout"
	// DashboardLayoutConfigObjectID is the one object id this kind uses.
	DashboardLayoutConfigObjectID = "default"

	DashboardLayoutSourceAPI = "api"

	maxDashboardLayoutKeys = 256
)

// DashboardPreviewLayout is the tile order and the hidden tiles of the preview grid.
type DashboardPreviewLayout struct {
	Order  []string `json:"order"`
	Hidden []string `json:"hidden"`
}

// DashboardLayoutPayload is the stored payload of dashboard.layout.
type DashboardLayoutPayload struct {
	Previews DashboardPreviewLayout `json:"previews"`
}

// Normalized returns p with both lists non-nil, so they encode as arrays.
func (p DashboardLayoutPayload) Normalized() DashboardLayoutPayload {
	if p.Previews.Order == nil {
		p.Previews.Order = []string{}
	}
	if p.Previews.Hidden == nil {
		p.Previews.Hidden = []string{}
	}
	return p
}

// EncodeDashboardLayoutPayload marshals p into the stored payload shape.
func EncodeDashboardLayoutPayload(p DashboardLayoutPayload) (string, error) {
	b, err := json.Marshal(p.Normalized())
	if err != nil {
		return "", fmt.Errorf("config: encode dashboard.layout payload: %w", err)
	}
	return string(b), nil
}

// DecodeDashboardLayoutPayload is [EncodeDashboardLayoutPayload]'s inverse.
func DecodeDashboardLayoutPayload(raw string) (DashboardLayoutPayload, error) {
	var p DashboardLayoutPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DashboardLayoutPayload{}, fmt.Errorf("config: decode dashboard.layout payload: %w", err)
	}
	return p.Normalized(), nil
}

// DashboardLayoutProblem is the operator's one line for a layout that cannot be saved, or "".
func DashboardLayoutProblem(p DashboardLayoutPayload) string {
	for _, list := range [][]string{p.Previews.Order, p.Previews.Hidden} {
		if len(list) > maxDashboardLayoutKeys {
			return fmt.Sprintf("The layout lists too many tiles. Keep it to %d or fewer.", maxDashboardLayoutKeys)
		}
		seen := make(map[string]bool, len(list))
		for _, key := range list {
			if key == "" {
				return "A tile key is empty. Remove it and save again."
			}
			if seen[key] {
				return fmt.Sprintf("The tile %q is listed twice. Remove the duplicate and save again.", key)
			}
			seen[key] = true
		}
	}
	return ""
}
