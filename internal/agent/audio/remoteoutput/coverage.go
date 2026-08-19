package remoteoutput

import pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"

// Policy is whether a remote output blocks the show when its evidence is
// incomplete. AUDIO-ENGINE section 8.1: an optional output only warns; a
// required output's policy evaluates coverage across every exact content
// hash in the pinned session revision.
type Policy string

const (
	PolicyOptional Policy = "optional"
	PolicyRequired Policy = "required"
)

// CoverageResult is what [EvidenceStore.Coverage] found for one
// destination against a set of required content hashes.
type CoverageResult struct {
	Satisfied bool
	// Missing lists, in the order given to Coverage, every content hash
	// with no qualifying evidence. Empty and non-nil when Satisfied.
	Missing []string
}

// Coverage reports whether dest has qualifying evidence — a record whose
// State is a member of acceptable — for every hash in contentHashes. A
// single [ProvisioningManuallyVerified] item never satisfies a
// multi-item requirement on its own: every hash needs its own record.
// acceptable is the caller's policy choice (e.g. requiring only
// [ProvisioningManuallyVerified] where the destination exposes no status
// API, or also accepting [ProvisioningAcknowledged] where it does).
func (s *EvidenceStore) Coverage(dest Destination, contentHashes []string, acceptable map[ProvisioningState]bool) CoverageResult {
	result := CoverageResult{Satisfied: true, Missing: []string{}}
	for _, hash := range contentHashes {
		rec, ok := s.Get(dest, hash)
		if !ok || !acceptable[rec.State] {
			result.Satisfied = false
			result.Missing = append(result.Missing, hash)
		}
	}
	return result
}

// Evaluation is [Decide]'s result: whether the destination blocks show
// dispatch and what an operator should be told.
type Evaluation struct {
	Blocking bool
	Reason   string
}

// Decide applies policy to coverage. [PolicyOptional] never blocks —
// incomplete coverage is reported as a non-blocking warning reason.
// [PolicyRequired] blocks whenever coverage is unsatisfied.
func Decide(policy Policy, coverage CoverageResult) Evaluation {
	if coverage.Satisfied {
		return Evaluation{Blocking: false}
	}
	if policy == PolicyRequired {
		return Evaluation{Blocking: true, Reason: "required remote output missing evidence for content hashes"}
	}
	return Evaluation{Blocking: false, Reason: "optional remote output missing evidence for content hashes"}
}

// RequiredContentHashes collects every distinct content hash a pinned
// session must have evidence for: every playlist item when playlist is
// non-nil, otherwise the single media reference. extra carries
// additional required assets such as an announcement not itself part of
// the playlist.
func RequiredContentHashes(playlist *pkgaudio.PlaylistRef, single *pkgaudio.MediaRef, extra ...pkgaudio.MediaRef) []string {
	seen := make(map[string]struct{})
	var hashes []string
	add := func(m pkgaudio.MediaRef) {
		if m.ContentHash == "" {
			return
		}
		if _, ok := seen[m.ContentHash]; ok {
			return
		}
		seen[m.ContentHash] = struct{}{}
		hashes = append(hashes, m.ContentHash)
	}
	if playlist != nil {
		for _, item := range playlist.Items {
			add(item.Media)
		}
	}
	if single != nil {
		add(*single)
	}
	for _, m := range extra {
		add(m)
	}
	return hashes
}
