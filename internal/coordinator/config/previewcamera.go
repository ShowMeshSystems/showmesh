package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	// PreviewCameraConfigKind is the stored kind behind /api/v1/config/preview.camera/{id}.
	PreviewCameraConfigKind = "preview.camera"
	// PreviewCameraURLCredentialField is the credentials-table field holding a camera's full stream URL.
	PreviewCameraURLCredentialField = "url"

	PreviewCameraSourceAPI = "api"

	maxPreviewCameraNameRunes = 80
)

// ValidatePreviewCameraObjectID validates a camera id.
func ValidatePreviewCameraObjectID(id string) *ValidationError {
	return ValidateShowObjectID("camera id", id)
}

// PreviewCamera is the non-secret shape of one preview.camera object.
type PreviewCamera struct {
	Name    string
	Enabled bool
}

// PreviewCameraPayload is the stored payload. URLSet and URLDisplay describe the
// stream URL, which lives in the credentials table and is never stored here.
type PreviewCameraPayload struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	URLSet     bool   `json:"urlSet"`
	URLDisplay string `json:"urlDisplay"`
}

// EncodePreviewCameraPayload marshals p into the stored payload shape.
func EncodePreviewCameraPayload(p PreviewCameraPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode preview.camera payload: %w", err)
	}
	return string(b), nil
}

// DecodePreviewCameraPayload is [EncodePreviewCameraPayload]'s inverse.
func DecodePreviewCameraPayload(raw string) (PreviewCameraPayload, error) {
	var p PreviewCameraPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return PreviewCameraPayload{}, fmt.Errorf("config: decode preview.camera payload: %w", err)
	}
	return p, nil
}

// PreviewCameraNameProblem is the operator's one line for a camera name that cannot be saved, or "".
func PreviewCameraNameProblem(name string) string {
	if strings.TrimSpace(name) == "" {
		return "The camera needs a name. Enter one and save again."
	}
	if utf8.RuneCountInString(name) > maxPreviewCameraNameRunes {
		return fmt.Sprintf("The camera name is too long. Use %d characters or fewer.", maxPreviewCameraNameRunes)
	}
	return ""
}

// PreviewCameraURLProblem is the operator's one line for a stream URL that cannot be saved, or "".
func PreviewCameraURLProblem(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") || u.Hostname() == "" {
		return "The camera address must start with rtsp:// or rtsps:// and name a host. Correct it and save again."
	}
	return ""
}

// PreviewCameraURLDisplay is the part of a stream URL safe to show: scheme, host and port.
func PreviewCameraURLDisplay(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
