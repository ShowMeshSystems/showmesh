package assetsync

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// audioTargets answers, for one node, which of a Cue's audio, LTC and
// announcement outputs that node is the target of (ADR-045 decision 1).
type audioTargets struct {
	// nodeID is the node every Owns* answer is about.
	nodeID string

	// defaultNode is the node an output with no explicit target resolves
	// to: the installation's sole program+ltc audio.node, or, when it has
	// none, its sole audio.node whatever that node's role. Empty when
	// neither rule names exactly one node, which leaves every untargeted
	// output unresolved rather than landing it on an arbitrary node.
	defaultNode string
}

// loadAudioTargets resolves the node an untargeted output belongs to.
// The installation's sole program+ltc audio.node is that node when one
// exists; only one may hold that role
// ([config.ValidateAudioNodeRoleUniqueness]), so the first match is the
// only match. An installation whose one audio node is program-only has no
// program+ltc node at all, and its authored Cues name no target, so a sole
// audio.node of any role takes untargeted outputs. Two or more nodes with
// no program+ltc among them leave an untargeted output genuinely
// ambiguous; it resolves to no node, and readiness names that.
func loadAudioTargets(ctx context.Context, st *store.Store, nodeID string) (audioTargets, error) {
	objs, err := st.ListConfigObjects(ctx, config.AudioNodeConfigKind)
	if err != nil {
		return audioTargets{}, fmt.Errorf("assetsync: list audio.node objects: %w", err)
	}
	t := audioTargets{nodeID: nodeID}
	var declared []string
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.AudioNodeConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return audioTargets{}, fmt.Errorf("assetsync: read audio.node %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeAudioNodePayload(rev.PayloadJSON)
		if verr != nil {
			return audioTargets{}, fmt.Errorf("assetsync: decode audio.node %q: %s", obj.ID, verr.Detail)
		}
		declared = append(declared, obj.ID)
		if payload.Role == config.AudioNodeRoleProgramLTC {
			t.defaultNode = obj.ID
			return t, nil
		}
	}
	if len(declared) == 1 {
		t.defaultNode = declared[0]
	}
	return t, nil
}

// Owns reports whether this node is the target of an output declaring
// target. An explicit target names exactly one node; an empty target
// resolves to the installation's sole program+ltc node, which is what
// keeps every one-node installation's existing Cues unchanged.
func (t audioTargets) Owns(target string) bool {
	if target != "" {
		return target == t.nodeID
	}
	return t.defaultNode != "" && t.defaultNode == t.nodeID
}
