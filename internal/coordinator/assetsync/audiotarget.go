package assetsync

import (
	"context"
	"errors"
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

// loadShowAudioNodes reads a "show" object's own audioNodes list (ADR-049
// decision 10), the same field [config.ResolveAudioNodes]'s second tier
// resolves against. A show with no active revision, or with no audioNodes
// recorded, reports an empty list — decision 10's step 2 then contributes
// nothing and resolution falls through to step 3.
func ShowAudioNodes(ctx context.Context, st *store.Store, showID string) ([]string, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowConfigKind, showID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("assetsync: get show %q: %w", showID, err)
	}
	if obj.CurrentRevision == 0 {
		return nil, nil
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowConfigKind, showID, obj.CurrentRevision)
	if err != nil {
		return nil, fmt.Errorf("assetsync: read show %q revision %d: %w", showID, obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeShowPayload(rev.PayloadJSON, alwaysTrue)
	if verr != nil {
		return nil, fmt.Errorf("assetsync: decode show %q: %s", showID, verr.Detail)
	}
	return payload.AudioNodes, nil
}

// DefaultAudioNodes reports the installation-wide default node, as a
// list, for [config.ResolveAudioNodes]'s own third tier: the sole
// program+ltc audio.node, or, when it has none, the sole audio.node of
// any role. Empty when neither rule names exactly one node. This is
// [loadAudioTargets]'s own defaultNode, exported for callers outside this
// package (show.action audio dispatch, night announcements) that need the
// default tier without a specific node's own membership question.
func DefaultAudioNodes(ctx context.Context, st *store.Store) ([]string, error) {
	t, err := loadAudioTargets(ctx, st, "")
	if err != nil {
		return nil, err
	}
	return t.defaultNodes(), nil
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

// OwnsAny is [Owns] widened to ADR-049's targets list, for outputs.audio
// and outputs.announcement: this node is owned when it appears anywhere in
// targets, and an empty targets list resolves to the same sole
// program+ltc default Owns applies to an empty single target.
//
// Deprecated: this is decision 7's own two-tier resolution (explicit
// targets, else the installation default) with no show.audioNodes tier.
// [OwnsResolved] is decision 10's replacement and is what every caller
// scoping outputs.audio/outputs.announcement by node must use now.
func (t audioTargets) OwnsAny(targets []string) bool {
	if len(targets) == 0 {
		return t.defaultNode != "" && t.defaultNode == t.nodeID
	}
	for _, target := range targets {
		if target == t.nodeID {
			return true
		}
	}
	return false
}

// defaultNodes is t's own single default node as a list, the shape
// [config.ResolveAudioNodes] takes for its own default tier.
func (t audioTargets) defaultNodes() []string {
	if t.defaultNode == "" {
		return nil
	}
	return []string{t.defaultNode}
}

// OwnsResolved is ADR-049 decision 10's own [Owns]/[OwnsAny] replacement:
// this node is owned when it appears in the list [config.ResolveAudioNodes]
// resolves for explicit/showAudioNodes/excludeNodes, using t's own default
// node for the third tier. Every consumer scoping a Cue's outputs.audio or
// outputs.announcement, a night bed, or a show.action audio target by node
// calls this, never [OwnsAny] directly, so the same three inputs always
// answer "does this node play this" identically to what
// [config.ResolveAudioNodes] itself returns for asset sync, readiness, and
// dispatch.
func (t audioTargets) OwnsResolved(explicit, showAudioNodes, excludeNodes []string) bool {
	resolved, _ := config.ResolveAudioNodes(explicit, showAudioNodes, excludeNodes, t.defaultNodes())
	for _, id := range resolved {
		if id == t.nodeID {
			return true
		}
	}
	return false
}
