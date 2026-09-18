package config

import (
	"encoding/json"
	"fmt"
)

// ValidationCodeExcludeNodesDuplicate is the refusal code for a repeated id
// inside excludeNodes (ADR-049 decision 10).
const ValidationCodeExcludeNodesDuplicate = "exclude-nodes-duplicate"

// AudioNodeResolution names which tier of ADR-049 decision 10's resolution
// order produced a resolved audio node list for one audio-bearing object.
type AudioNodeResolution string

const (
	// AudioNodeResolutionExplicit is decision 10 step 1: the object's own
	// non-empty target list.
	AudioNodeResolutionExplicit AudioNodeResolution = "explicit"
	// AudioNodeResolutionShow is decision 10 step 2: the show's audioNodes
	// minus the object's own excludeNodes.
	AudioNodeResolutionShow AudioNodeResolution = "show"
	// AudioNodeResolutionDefault is decision 10 step 3: the installation's
	// program+ltc node, or its sole audio.node.
	AudioNodeResolutionDefault AudioNodeResolution = "default"
)

// ResolveAudioNodes is ADR-049 decision 10's single resolution function.
// Every consumer that decides where a Cue's audio or announcement plays, a
// night bed plays, or a show.action's audio target dispatches to calls this
// with the same three inputs, so the resolved list is identical wherever
// it is asked for the same object:
//
//  1. explicit, the object's own already-decoded target list (show.cue
//     outputs.audio/announcement targets, a night bed's targets, or a
//     show.action audio target's audioNodeId list), when non-empty.
//  2. Otherwise showAudioNodes minus excludeNodes.
//  3. Otherwise defaultNodes, today's fallback (the installation's
//     program+ltc node, or its sole audio.node) computed by the caller,
//     since resolving it needs store access this package does not have.
func ResolveAudioNodes(explicit, showAudioNodes, excludeNodes, defaultNodes []string) ([]string, AudioNodeResolution) {
	if len(explicit) > 0 {
		return append([]string(nil), explicit...), AudioNodeResolutionExplicit
	}
	if len(showAudioNodes) > 0 {
		excluded := make(map[string]bool, len(excludeNodes))
		for _, id := range excludeNodes {
			excluded[id] = true
		}
		var out []string
		for _, id := range showAudioNodes {
			if !excluded[id] {
				out = append(out, id)
			}
		}
		return out, AudioNodeResolutionShow
	}
	return append([]string(nil), defaultNodes...), AudioNodeResolutionDefault
}

// decodeExcludeNodes reads an optional "excludeNodes" list under fields
// (ADR-049 decision 10) for one of the four audio-bearing shapes.
// ownExplicit is that shape's own already-decoded explicit target list
// (targets, or a show.action audio target's audioNodeId list); excludeNodes
// together with a non-empty ownExplicit is refused, because decision 10's
// step 1 makes an exclude list meaningless once the object names its own
// nodes. When showAudioNodes is nil, the membership and completeness
// checks below are skipped: this is the non-validating, stored-row
// read-back posture [decodeStoredShowCueTargets] documents, used where no
// live show.audioNodes list is available. An explicitly empty
// showAudioNodes ([]string{}) is a real show with no audio nodes and is
// checked for real, refusing any excludeNodes entry.
func decodeExcludeNodes(fields map[string]json.RawMessage, path string, ownExplicit []string, showAudioNodes []string, validate bool) ([]string, *ValidationError) {
	ids, verr := decodeOptionalStringList(fields, "excludeNodes", path+".excludeNodes")
	if verr != nil || ids == nil {
		return nil, verr
	}
	excludeNodes := *ids
	if len(excludeNodes) == 0 {
		return nil, nil
	}
	if len(ownExplicit) > 0 {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: path + ".excludeNodes",
			Detail: "excludeNodes applies only when the show's audio nodes are inherited; clear targets first",
		}
	}
	seen := make(map[string]bool, len(excludeNodes))
	for i, id := range excludeNodes {
		field := fmt.Sprintf("%s.excludeNodes[%d]", path, i)
		if id == "" {
			return nil, &ValidationError{
				Code: ValidationCodeFieldEmpty, Field: field,
				Detail: fmt.Sprintf("%s must not be empty", field),
			}
		}
		if seen[id] {
			return nil, &ValidationError{
				Code: ValidationCodeExcludeNodesDuplicate, Field: field,
				Detail: fmt.Sprintf("%s repeats audio node id %q; list each node once", field, id),
			}
		}
		seen[id] = true
	}
	if !validate {
		return excludeNodes, nil
	}
	inShowList := make(map[string]bool, len(showAudioNodes))
	for _, id := range showAudioNodes {
		inShowList[id] = true
	}
	for i, id := range excludeNodes {
		if !inShowList[id] {
			field := fmt.Sprintf("%s.excludeNodes[%d]", path, i)
			return nil, &ValidationError{
				Code: ValidationCodeFieldUnknownReference, Field: field,
				Detail: fmt.Sprintf("%s names %q, which is not in the show's audio nodes", field, id),
			}
		}
	}
	if len(showAudioNodes) > 0 && len(excludeNodes) == len(showAudioNodes) {
		return nil, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: path + ".excludeNodes",
			Detail: fmt.Sprintf("%s excludes every one of the show's audio nodes; leave at least one node playing", path+".excludeNodes"),
		}
	}
	return excludeNodes, nil
}
