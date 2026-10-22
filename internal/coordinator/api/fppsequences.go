package api

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"sync"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

const fppSequenceListUnreachable = "This FPP did not answer with its sequence list. Check that it is on and reachable."

var fppSequencesReadScopes = []identity.Scope{identity.ScopeFPPRead, identity.ScopeConfigWrite}

// handleFPPSequences serves GET /api/v1/fpp/sequences: the union of every
// configured FPP's sequence files, read from FPP itself.
func (h *handlers) handleFPPSequences(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	endpoints, err := currentFPPEndpoints(r.Context(), h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp endpoints", err)
		return
	}
	lists := make([][]string, len(endpoints))
	errs := make([]error, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i], errs[i] = listFPPSequences(r.Context(), ep.URL)
		}()
	}
	wg.Wait()

	holders := map[string][]string{}
	resp := v1.FPPSequenceListResponse{ServerTime: formatTime(now), Sequences: []v1.FPPSequenceFile{}, Instances: []v1.FPPSequenceListInstance{}}
	for i, ep := range endpoints {
		inst := v1.FPPSequenceListInstance{ID: ep.ID}
		if errs[i] != nil {
			h.logger.Warn("fpp sequence list read failed", "instance_id", ep.ID, "error", errs[i])
			inst.Error = fppSequenceListUnreachable
		}
		resp.Instances = append(resp.Instances, inst)
		for _, name := range lists[i] {
			if name != "" && !slices.Contains(holders[name], ep.ID) {
				holders[name] = append(holders[name], ep.ID)
			}
		}
	}
	for name, ids := range holders {
		resp.Sequences = append(resp.Sequences, v1.FPPSequenceFile{Filename: name, Instances: ids})
	}
	sort.Slice(resp.Sequences, func(i, j int) bool { return resp.Sequences[i].Filename < resp.Sequences[j].Filename })
	jsonWrite(w, resp)
}

func listFPPSequences(ctx context.Context, baseURL string) ([]string, error) {
	client, err := fppcommand.New(baseURL, fppcommand.Options{})
	if err != nil {
		return nil, err
	}
	return client.ListSequences(ctx)
}
