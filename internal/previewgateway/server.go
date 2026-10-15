package previewgateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxSDPBytes     = 64 << 10
	snapshotTimeout = 15 * time.Second
	answerTimeout   = 15 * time.Second
)

// Server is the gateway HTTP API.
type Server struct {
	token   [sha256.Size]byte
	manager *Manager
	sources SourceProvider
	engine  Engine
}

func NewServer(token string, m *Manager, sources SourceProvider, engine Engine) *Server {
	return &Server{token: sha256.Sum256([]byte(token)), manager: m, sources: sources, engine: engine}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /v1/sources", s.auth(s.getSources))
	mux.HandleFunc("PUT /v1/streams", s.auth(s.putStreams))
	mux.HandleFunc("GET /v1/streams", s.auth(s.getStreams))
	mux.HandleFunc("POST /v1/streams/{key}/webrtc", s.auth(s.postWebRTC))
	mux.HandleFunc("GET /v1/streams/{key}/snapshot", s.auth(s.getSnapshot))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(got))
		if !ok || subtle.ConstantTimeCompare(sum[:], s.token[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "The gateway token was not accepted. Set the same token on the coordinator and the gateway.")
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) getSources(w http.ResponseWriter, _ *http.Request) {
	list := s.sources.Sources()
	if list == nil {
		list = []Source{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) putStreams(w http.ResponseWriter, r *http.Request) {
	specs, err := ParseStreams(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.manager.Set(r.Context(), specs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getStreams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.manager.Streams(r.Context()))
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (key, id string, ok bool) {
	key = r.PathValue("key")
	id, _, found := s.manager.Lookup(key)
	if !found {
		writeError(w, http.StatusNotFound, "The gateway does not have this stream. Send the stream list again.")
		return "", "", false
	}
	return key, id, true
}

func (s *Server) postWebRTC(w http.ResponseWriter, r *http.Request) {
	key, id, ok := s.lookup(w, r)
	if !ok {
		return
	}
	offer, err := io.ReadAll(io.LimitReader(r.Body, maxSDPBytes+1))
	if err != nil || len(offer) == 0 || len(offer) > maxSDPBytes || !strings.HasPrefix(string(offer), "v=0") {
		writeError(w, http.StatusBadRequest, "The request body is not a WebRTC offer. Send the browser's SDP offer as application/sdp.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), answerTimeout)
	defer cancel()
	answer, err := s.engine.WebRTC(ctx, id, offer)
	if err != nil {
		s.manager.NoteResult(key, false)
		writeError(w, http.StatusBadGateway, "The gateway could not start this video. Try again, or check the source.")
		return
	}
	s.manager.NoteResult(key, true)
	w.Header().Set("Content-Type", "application/sdp")
	_, _ = w.Write(answer)
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	key, id, ok := s.lookup(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), snapshotTimeout)
	defer cancel()
	img, err := s.engine.Snapshot(ctx, id)
	if err != nil {
		s.manager.NoteResult(key, false)
		code := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		}
		writeError(w, code, "The gateway has no picture from this source. Check that the source is running.")
		return
	}
	s.manager.NoteResult(key, true)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(img)
}
