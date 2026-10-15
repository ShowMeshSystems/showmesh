package config

import (
	"encoding/json"
	"fmt"
	"net/url"
)

const (
	// PreviewGatewayConfigKind is the stored kind behind /api/v1/config/preview.gateway.
	PreviewGatewayConfigKind = "preview.gateway"
	// PreviewGatewayConfigObjectID is the one object id this kind uses.
	PreviewGatewayConfigObjectID = "default"
	// PreviewGatewayTokenCredentialField is the credentials-table field holding the gateway token.
	PreviewGatewayTokenCredentialField = "token"

	PreviewGatewaySourceAPI = "api"
)

// PreviewGatewayConfig is the non-secret shape of preview.gateway. An empty URL means previews are off.
type PreviewGatewayConfig struct {
	URL string
}

// Configured reports whether previews are on.
func (c PreviewGatewayConfig) Configured() bool { return c.URL != "" }

// PreviewGatewayPayload is the stored payload. TokenSet is a reference, never the token.
type PreviewGatewayPayload struct {
	URL      string `json:"url"`
	TokenSet bool   `json:"tokenSet"`
}

// EncodePreviewGatewayPayload marshals cfg into the stored payload shape.
func EncodePreviewGatewayPayload(cfg PreviewGatewayConfig, tokenSet bool) (string, error) {
	b, err := json.Marshal(PreviewGatewayPayload{URL: cfg.URL, TokenSet: tokenSet})
	if err != nil {
		return "", fmt.Errorf("config: encode preview.gateway payload: %w", err)
	}
	return string(b), nil
}

// DecodePreviewGatewayPayload is [EncodePreviewGatewayPayload]'s inverse.
func DecodePreviewGatewayPayload(raw string) (PreviewGatewayConfig, bool, error) {
	var p PreviewGatewayPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return PreviewGatewayConfig{}, false, fmt.Errorf("config: decode preview.gateway payload: %w", err)
	}
	return PreviewGatewayConfig{URL: p.URL}, p.TokenSet, nil
}

// PreviewGatewayProblem is the operator's one line for a gateway address that cannot be saved, or "".
func PreviewGatewayProblem(cfg PreviewGatewayConfig) string {
	if cfg.URL == "" {
		return ""
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "The gateway address must start with http:// or https:// and name a host. Correct it and save again."
	}
	if u.User != nil {
		return "The gateway address must not contain a login. Remove it and set the token separately."
	}
	return ""
}
