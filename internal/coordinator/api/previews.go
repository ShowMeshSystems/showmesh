package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/preview"
)

const maxPreviewOfferBytes = 64 * 1024

// PreviewService is what the preview routes need from the preview gateway
// service. Offer and Snapshot return [preview.ErrUnknownKey] for a key no tile
// has and a [*preview.UnavailableError] when the gateway cannot serve it.
type PreviewService interface {
	Overview(ctx context.Context) (preview.Overview, error)
	Offer(ctx context.Context, key string, offer []byte) ([]byte, error)
	Snapshot(ctx context.Context, key string) ([]byte, error)
	Notify()
}

// noPreviewService is [Dependencies.Previews]'s nil-safe default: no gateway.
type noPreviewService struct{}

func (noPreviewService) Overview(context.Context) (preview.Overview, error) {
	return preview.Overview{Gateway: preview.GatewayStatus{State: preview.GatewayNotConfigured}, Tiles: []preview.Tile{}}, nil
}

func (noPreviewService) Offer(context.Context, string, []byte) ([]byte, error) {
	return nil, &preview.UnavailableError{Message: "No preview gateway is set up. Add its address in Settings to see previews."}
}

func (noPreviewService) Snapshot(context.Context, string) ([]byte, error) {
	return nil, &preview.UnavailableError{Message: "No preview gateway is set up. Add its address in Settings to see previews."}
}

func (noPreviewService) Notify() {}

func (h *handlers) handleListPreviews(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ov, err := h.deps.Previews.Overview(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list previews", err)
		return
	}
	jsonWrite(w, mapPreviewsResponse(now, ov))
}

// writePreviewError answers a failed Offer or Snapshot; it reports whether err was handled.
func (h *handlers) writePreviewError(w http.ResponseWriter, now time.Time, err error, what string) {
	var unavailable *preview.UnavailableError
	switch {
	case errors.Is(err, preview.ErrUnknownKey):
		writeProblem(w, h.logger, now, resourceNotFoundProblem("No preview tile has this key. Refresh the dashboard and try again."))
	case errors.As(err, &unavailable):
		writeProblem(w, h.logger, now, v1.Problem{
			Type: ProblemTypePreviewGatewayUnavailable, Title: "Preview gateway unavailable",
			Status: http.StatusServiceUnavailable, Detail: unavailable.Message,
		})
	default:
		h.writeInternalError(w, now, what, err)
	}
}

func (h *handlers) handlePreviewOffer(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	offer, err := io.ReadAll(io.LimitReader(r.Body, maxPreviewOfferBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read preview offer", err)
		return
	}
	if len(offer) == 0 || len(offer) > maxPreviewOfferBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("The offer is empty or too large. Send the browser's session description as the request body."))
		return
	}
	answer, err := h.deps.Previews.Offer(r.Context(), r.PathValue("key"), offer)
	if err != nil {
		h.writePreviewError(w, now, err, "preview offer")
		return
	}
	w.Header().Set("Content-Type", "application/sdp")
	_, _ = w.Write(answer)
}

func (h *handlers) handlePreviewSnapshot(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	frame, err := h.deps.Previews.Snapshot(r.Context(), r.PathValue("key"))
	if err != nil {
		h.writePreviewError(w, now, err, "preview snapshot")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(frame)
}

func mapPreviewsResponse(now time.Time, ov preview.Overview) v1.PreviewsResponse {
	tiles := make([]v1.PreviewTile, 0, len(ov.Tiles))
	for _, t := range ov.Tiles {
		tiles = append(tiles, v1.PreviewTile{
			Key: t.Key, Kind: t.Kind, Label: t.Label,
			NodeID: nonEmptyStrPtr(t.NodeID), SurfaceID: nonEmptyStrPtr(t.SurfaceID), CameraID: nonEmptyStrPtr(t.CameraID),
			State: t.State, Message: t.Message, Hidden: t.Hidden, ObservedAt: formatTime(t.ObservedAt),
		})
	}
	var discovery *v1.PreviewNDIDiscovery
	if d := ov.Gateway.NDIDiscovery; d != nil {
		discovery = &v1.PreviewNDIDiscovery{State: d.State, Message: d.Message}
	}
	return v1.PreviewsResponse{
		ServerTime: formatTime(now),
		Gateway: v1.PreviewGatewayStatus{
			State: ov.Gateway.State, Message: ov.Gateway.Message, NDIDiscovery: discovery, ObservedAt: formatTime(ov.Gateway.ObservedAt),
		},
		Tiles: tiles,
	}
}
