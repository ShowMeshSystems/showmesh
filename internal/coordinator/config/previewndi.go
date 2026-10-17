package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/showmeshsystems/showmesh/internal/previewgateway"
)

const (
	// PreviewNDIConfigKind is the stored kind behind /api/v1/config/preview.ndi/{id}.
	PreviewNDIConfigKind = "preview.ndi"

	maxPreviewNDILabelRunes = 80
)

// ValidatePreviewNDIObjectID validates a hand-added NDI source id.
func ValidatePreviewNDIObjectID(id string) *ValidationError {
	return ValidateShowObjectID("NDI source id", id)
}

// PreviewNDIPayload is the stored payload of one NDI source added by hand.
// Address is the sender's host:port, or empty to find it by name.
type PreviewNDIPayload struct {
	Label   string `json:"label"`
	NDIName string `json:"ndiName"`
	Address string `json:"address"`
	Enabled bool   `json:"enabled"`
}

// EncodePreviewNDIPayload marshals p into the stored payload shape.
func EncodePreviewNDIPayload(p PreviewNDIPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode preview.ndi payload: %w", err)
	}
	return string(b), nil
}

// DecodePreviewNDIPayload is [EncodePreviewNDIPayload]'s inverse.
func DecodePreviewNDIPayload(raw string) (PreviewNDIPayload, error) {
	var p PreviewNDIPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return PreviewNDIPayload{}, fmt.Errorf("config: decode preview.ndi payload: %w", err)
	}
	return p, nil
}

// PreviewNDIProblem is the operator's one line for a source that cannot be saved, or "".
func PreviewNDIProblem(p PreviewNDIPayload) string {
	if utf8.RuneCountInString(p.Label) > maxPreviewNDILabelRunes {
		return fmt.Sprintf("The label is too long. Use %d characters or fewer.", maxPreviewNDILabelRunes)
	}
	if strings.TrimSpace(p.NDIName) == "" {
		return "The NDI source needs its NDI name. Enter it and save again."
	}
	if err := previewgateway.ValidateNDIName(p.NDIName); err != nil {
		return err.Error()
	}
	if p.Address != "" {
		if err := previewgateway.ValidateNDIAddress(p.Address); err != nil {
			return err.Error()
		}
	}
	return ""
}
