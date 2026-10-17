package assetsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// RestingSequence returns the resting sequence of the active night session
// when that session belongs to showID. ok is false when there is none.
func RestingSequence(ctx context.Context, st *store.Store, showID string) (sequence string, ok bool, err error) {
	activeObj, err := st.GetConfigObject(ctx, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get night.session.active: %w", err)
	}
	activeRev, err := st.GetConfigRevision(ctx, config.NightSessionActiveConfigKind, activeObj.ID, activeObj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read night.session.active revision %d: %w", activeObj.CurrentRevision, err)
	}
	var pointer config.NightSessionActivePayload
	if err := json.Unmarshal([]byte(activeRev.PayloadJSON), &pointer); err != nil || pointer.Session == "" {
		return "", false, nil
	}

	obj, err := st.GetConfigObject(ctx, config.NightSessionConfigKind, pointer.Session)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get night.session %q: %w", pointer.Session, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.NightSessionConfigKind, obj.ID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read night.session %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
	}
	var payload config.NightSessionPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return "", false, nil
	}
	ref := payload.Resting.TimelineAsset
	if payload.Show != showID || ref.Show != showID || ref.Sequence == "" {
		return "", false, nil
	}
	return ref.Sequence, true, nil
}

// restingRenderEntry builds the resting sequence's catalog entry for a node
// that holds a surface and an FSEQ for that sequence (ADR-061 decisions 1,
// 2 and 6). A Cue that already renders the sequence wins, and is named.
func restingRenderEntry(ctx context.Context, st *store.Store, showID string, nodeHasSurface bool, entries []cuecatalog.Entry, assetsBySequence map[string][]ExpectedAsset) (*cuecatalog.Entry, string, error) {
	if !nodeHasSurface {
		return nil, "", nil
	}
	sequence, ok, err := RestingSequence(ctx, st, showID)
	if err != nil || !ok {
		return nil, "", err
	}
	for _, e := range entries {
		if e.Outputs.Render != nil && e.Outputs.Render.Sequence == sequence {
			return nil, e.CueID, nil
		}
	}
	filename, hashes := resolveAssetFor(assetsBySequence, sequence, "fseq")
	if filename == "" || len(hashes) == 0 {
		return nil, "", nil
	}
	return &cuecatalog.Entry{
		CueID:       cuecatalog.RestingEntryID(sequence),
		CueRevision: cuecatalog.RestingEntryRevision,
		Outputs: cuecatalog.Outputs{Render: &cuecatalog.RenderOutput{
			Sequence: sequence, Filename: filename, AssetHashes: hashes,
		}},
		Triggers: []string{},
	}, "", nil
}
