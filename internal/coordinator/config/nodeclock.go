package config

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// This file is the per-node kind (ADR-039, IDENTIFIER-REGISTER.md's
// "node.clock" reservation — reserved 2026-08-28 before Track I seam I1
// started): which of the three PTP providers this node runs, its
// interface, declared domain, role policy, and holdover limit. A
// collection, mirroring audio.node exactly: the object id is the node id
// itself, and it is a DIFFERENT kind from audio.node deliberately — see
// the register's own note on why the media clock is a node-level
// facility, not an audio-node field.

// NodeClockConfigKind is config_objects.kind and config_revisions.kind
// for a node.clock object.
const NodeClockConfigKind = "node.clock"

// ValidateNodeClockObjectID validates a node.clock object id against the
// same syntax a node id must satisfy, matching
// [ValidateAudioNodeObjectID]'s identical reuse one kind over.
func ValidateNodeClockObjectID(id string) *ValidationError {
	return ValidateShowObjectID("node id", id)
}

// The three provider kinds this seam ships — RES-019 section 1's
// ShowMesh-managed linuxptp, externally-owned linuxptp, and FPP-observed
// providers.
const (
	NodeClockProviderManaged  = "managed"
	NodeClockProviderExternal = "external"
	NodeClockProviderFPP      = "fpp"
)

var nodeClockProviders = map[string]bool{
	NodeClockProviderManaged:  true,
	NodeClockProviderExternal: true,
	NodeClockProviderFPP:      true,
}

// DefaultNodeClockHoldoverLimitSeconds mirrors
// internal/agent/clock.HoldoverLimitDefault (60s) — the wire default when
// an operator omits holdoverLimitSeconds. Kept as an independent copy,
// not an import: this package has no dependency on internal/agent,
// matching every other coordinator/agent wire-shape boundary in this
// codebase.
const DefaultNodeClockHoldoverLimitSeconds = 60

var nodeClockTopLevelKeys = map[string]bool{
	"provider": true, "interface": true, "domain": true,
	"clientOnly": true, "holdoverLimitSeconds": true,
	"priority1": true, "hardwareTimestamping": true,
	"externalUdsAddress": true, "fppBaseUrl": true,
	"phcDevice": true,
}

// phcDevicePattern is the only shape [DecodeNodeClockPayload] accepts for
// phcDevice: a Linux PTP hardware clock device node. Anything else (a
// symlink, a udev alias, a bare interface name) is refused rather than
// guessed at, matching every PTP hardware clock this codebase already
// reads ([clock.PHCIndexForInterface]'s own "/dev/ptp%d" construction).
var phcDevicePattern = regexp.MustCompile(`^/dev/ptp[0-9]+$`)

// NodeClockPayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [NodeClockConfigKind].
type NodeClockPayload struct {
	// Provider selects which of the three concrete providers this node
	// runs: [NodeClockProviderManaged], [NodeClockProviderExternal], or
	// [NodeClockProviderFPP].
	Provider string `json:"provider"`

	// Interface is the network interface this node's clock provider
	// observes (managed and external) or, for FPP, the interface a
	// caller reading this configuration should attribute a locally
	// attached PHC to, if any.
	Interface string `json:"interface"`

	// Domain is the operator-declared PTP domain number (0-255), used
	// both to configure the managed provider and to detect a mismatch
	// (RES-019 section 9) against what external/FPP providers actually
	// observe.
	Domain int `json:"domain"`

	// ClientOnly declares this node's role policy for the managed
	// provider: true when the operator declares an external domain
	// (RES-019 section 1) — this node never attempts to become the
	// domain's own grandmaster. Meaningless for external/FPP providers,
	// which never run their own ptp4l.
	ClientOnly bool `json:"clientOnly,omitempty"`

	// HoldoverLimitSeconds bounds how long a lost lock is reported as
	// StateHoldover before the node gives up and reports
	// StateUnsynchronized (RES-019 section 9). Defaults to
	// [DefaultNodeClockHoldoverLimitSeconds] when zero.
	HoldoverLimitSeconds int `json:"holdoverLimitSeconds,omitempty"`

	// Priority1 overrides the managed provider's BMCA priority1 (RES-019
	// section 1's "priority1 248 in auto so professional gear at 128
	// wins"). Zero selects the provider's own default (248). Managed
	// only.
	Priority1 int `json:"priority1,omitempty"`

	// HardwareTimestamping requests hardware timestamping with a
	// software fallback attempt (RES-019 section 1). Managed only.
	HardwareTimestamping bool `json:"hardwareTimestamping,omitempty"`

	// ExternalUDSAddress overrides the read-only management socket path
	// an external provider polls. Empty selects
	// clock.DefaultExternalUDSAddress. External only.
	ExternalUDSAddress string `json:"externalUdsAddress,omitempty"`

	// FPPBaseURL is the FPP 10 host's own base URL — required when
	// Provider is [NodeClockProviderFPP].
	FPPBaseURL string `json:"fppBaseUrl,omitempty"`

	// PHCDevice is the operator-declared PTP hardware clock device (e.g.
	// "/dev/ptp0") that the externally-owned ptp4l (or phc2sys) this node
	// observes keeps disciplined to PTP time. External only.
	//
	// This exists because linuxptp's read-only management socket cannot
	// answer "did the observed ptp4l reach hardware timestamping":
	// PORT_PROPERTIES_NP, the one management set that carries a
	// timestamping field, is refused on any socket but ptp4l's read-write
	// one (verified against linuxptp 4.2's clock_manage(), which checks
	// `p != c->uds_rw_port` before answering it, and confirmed live
	// against a real ptp4l instance: the identical query returns
	// MANAGEMENT_ERROR_STATUS on uds_ro_address and the real fields on
	// uds_rw_address). The external provider is deliberately never handed
	// the read-write socket (RES-019 section 5.3: it only ever observes),
	// so this is the one signal ShowMesh can act on instead: an explicit
	// operator declaration, matching audio.node's own clockDomain/
	// clockDomainProvenance precedent for a fact this codebase cannot
	// verify itself. The agent refuses media time outright whenever this
	// does not match the interface's own PHC (ETHTOOL_GET_TS_INFO),
	// rather than silently ignoring the mismatch.
	PHCDevice string `json:"phcDevice,omitempty"`
}

// EncodeNodeClockPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeNodeClockPayload); this function does not re-validate.
func EncodeNodeClockPayload(p NodeClockPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode node.clock payload: %w", err)
	}
	return string(b), nil
}

// DecodeNodeClockPayload parses and validates raw's STRUCTURE: provider,
// interface, and domain are required; fppBaseUrl is required exactly
// when provider is "fpp"; every other field is optional with the
// documented default.
func DecodeNodeClockPayload(raw string) (NodeClockPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return NodeClockPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, nodeClockTopLevelKeys); verr != nil {
		return NodeClockPayload{}, verr
	}

	provider, verr := decodeRequiredString(top, "provider", "provider")
	if verr != nil {
		return NodeClockPayload{}, verr
	}
	if !nodeClockProviders[provider] {
		return NodeClockPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "provider",
			Detail: fmt.Sprintf("provider %q is not one of %q, %q, %q", provider, NodeClockProviderManaged, NodeClockProviderExternal, NodeClockProviderFPP),
		}
	}

	iface, verr := decodeRequiredString(top, "interface", "interface")
	if verr != nil {
		return NodeClockPayload{}, verr
	}

	domain, verr := decodeRequiredInt(top, "domain", "domain")
	if verr != nil {
		return NodeClockPayload{}, verr
	}
	if domain < 0 || domain > 255 {
		return NodeClockPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "domain",
			Detail: "domain must be a PTP domain number between 0 and 255",
		}
	}

	clientOnly, verr := decodeDefaultedBool(top, "clientOnly", "clientOnly", false)
	if verr != nil {
		return NodeClockPayload{}, verr
	}
	hardwareTimestamping, verr := decodeDefaultedBool(top, "hardwareTimestamping", "hardwareTimestamping", false)
	if verr != nil {
		return NodeClockPayload{}, verr
	}

	holdoverLimitSeconds := DefaultNodeClockHoldoverLimitSeconds
	if _, present := top["holdoverLimitSeconds"]; present {
		v, verr := decodeRequiredInt(top, "holdoverLimitSeconds", "holdoverLimitSeconds")
		if verr != nil {
			return NodeClockPayload{}, verr
		}
		if v <= 0 {
			return NodeClockPayload{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "holdoverLimitSeconds",
				Detail: "holdoverLimitSeconds must be positive",
			}
		}
		holdoverLimitSeconds = v
	}

	priority1 := 0
	if _, present := top["priority1"]; present {
		v, verr := decodeRequiredInt(top, "priority1", "priority1")
		if verr != nil {
			return NodeClockPayload{}, verr
		}
		if v < 0 || v > 255 {
			return NodeClockPayload{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "priority1",
				Detail: "priority1 must be between 0 and 255",
			}
		}
		priority1 = v
	}

	externalUDS, verr := decodeOptionalString(top, "externalUdsAddress", "externalUdsAddress")
	if verr != nil {
		return NodeClockPayload{}, verr
	}

	fppBaseURL, verr := decodeOptionalString(top, "fppBaseUrl", "fppBaseUrl")
	if verr != nil {
		return NodeClockPayload{}, verr
	}
	if provider == NodeClockProviderFPP && fppBaseURL == "" {
		return NodeClockPayload{}, &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "fppBaseUrl",
			Detail: "fppBaseUrl is required when provider is \"fpp\"",
		}
	}

	phcDevice, verr := decodeOptionalString(top, "phcDevice", "phcDevice")
	if verr != nil {
		return NodeClockPayload{}, verr
	}
	if phcDevice != "" {
		if provider != NodeClockProviderExternal {
			return NodeClockPayload{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "phcDevice",
				Detail: fmt.Sprintf("phcDevice must be absent unless provider is %q: an ignored field would read as an applied one", NodeClockProviderExternal),
			}
		}
		if !phcDevicePattern.MatchString(phcDevice) {
			return NodeClockPayload{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "phcDevice",
				Detail: fmt.Sprintf("phcDevice %q must match %s (a PTP hardware clock device path, e.g. \"/dev/ptp0\")", phcDevice, phcDevicePattern.String()),
			}
		}
	}

	return NodeClockPayload{
		Provider: provider, Interface: iface, Domain: domain,
		ClientOnly: clientOnly, HoldoverLimitSeconds: holdoverLimitSeconds,
		Priority1: priority1, HardwareTimestamping: hardwareTimestamping,
		ExternalUDSAddress: externalUDS, FPPBaseURL: fppBaseURL,
		PHCDevice: phcDevice,
	}, nil
}
