package clock

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultExternalUDSAddress is linuxptp's own documented default read-only
// management socket path (RES-019: "uds_ro_address, default
// /var/run/ptp/ptp4lro, mode 0666, GET-only").
const DefaultExternalUDSAddress = "/var/run/ptp/ptp4lro"

// ExternalConfig configures [NewExternalProvider].
type ExternalConfig struct {
	Interface string

	// Domain is this node's DECLARED domain (an operator value, used only
	// to pass to pmc — see [runPMC]'s own doc comment on why a mismatched
	// domain silently gets no response at all). The provider's own raw
	// reading of the OBSERVED domain comes from DEFAULT_DATA_SET, not
	// from this field.
	Domain int

	// UDSAddress is the read-only management socket to query. Defaults
	// to [DefaultExternalUDSAddress] when empty.
	UDSAddress string

	// LocalSocketDir is a directory this provider is already known to be
	// able to write, offered to pmc's own -i client socket as a fallback
	// candidate (see [pmcLocalSocketDirs]) behind systemd's
	// RuntimeDirectory/StateDirectory and ahead of the OS temp directory.
	// Optional: this provider owns no directory of its own, so it is
	// only as good as whatever the caller already knows, e.g. the
	// agent's configured asset directory.
	LocalSocketDir string

	// PHCDevice is the operator's own declaration of which PTP hardware
	// clock (e.g. "/dev/ptp0") the externally-owned ptp4l this provider
	// observes keeps disciplined to PTP time. Empty means no declaration:
	// [ExternalProvider.Now] keeps refusing to read a PHC exactly as it
	// did before this field existed. See [ExternalProvider.Now]'s own doc
	// comment for why an operator declaration, rather than a live
	// protocol read, is what this provider acts on.
	PHCDevice string
}

// ExternalProvider observes an externally-owned ptp4l instance's
// read-only management socket. It never starts, stops, reconfigures, or
// otherwise touches the observed ptp4l process — RES-019 section 5.3:
// exactly one component owns ptp4l on an interface, and this is never it.
type ExternalProvider struct {
	cfg ExternalConfig
}

// NewExternalProvider builds an ExternalProvider. Interface and Domain
// are used for reporting and pmc targeting only; presence of the socket
// itself is what [ExternalProvider.Poll] actually checks.
func NewExternalProvider(cfg ExternalConfig) *ExternalProvider {
	if cfg.UDSAddress == "" {
		cfg.UDSAddress = DefaultExternalUDSAddress
	}
	return &ExternalProvider{cfg: cfg}
}

func (p *ExternalProvider) Kind() ProviderKind { return ProviderExternal }
func (p *ExternalProvider) Interface() string  { return p.cfg.Interface }
func (p *ExternalProvider) Close() error       { return nil }

// Now serves media time from an interface with no PHC at all (necessarily
// software timestamped) or from a PHC the operator has explicitly
// declared in [ExternalConfig.PHCDevice] and that matches the interface's
// own PHC. Every other case is refused.
//
// RES-019 section 5.3 gives this provider two time sources, the PHC in
// hardware timestamping mode and "the disciplined system clock in
// software mode". Which of the two applies depends on the timestamping
// mode the OBSERVED ptp4l actually reached, and nothing on the read-only
// management socket reports that mode: PORT_PROPERTIES_NP is the one
// linuxptp management set that carries a timestamping field, and it is
// refused on any socket but ptp4l's own read-write one (verified against
// linuxptp 4.2's clock_manage(), which checks the request came in on
// c->uds_rw_port before answering PORT_PROPERTIES_NP, and confirmed live
// against a real ptp4l instance: the identical query returns
// MANAGEMENT_ERROR_STATUS on the read-only socket and the real fields on
// the read-write one). This provider is deliberately never handed the
// read-write socket (RES-019 section 5.3: it only ever observes, never
// touches, the ptp4l it does not own), so no in-protocol signal is
// available to it at all.
//
// PHCDevice is the answer: an explicit, narrowly-scoped operator
// attestation, the same trust model audio.node's own clockDomain/
// clockDomainProvenance already uses in this codebase for a fact ShowMesh
// cannot verify itself. Left empty, this provider refuses exactly as it
// did before this field existed — a PHC-bearing interface with no
// declaration is still refused, never inferred from the PHC merely
// existing or from the port being locked.
//
// An interface with NO PHC settles the hardware-vs-software question on
// its own regardless of any declaration: ptp4l cannot reach hardware
// timestamping without a PHC, so the instance being observed is
// necessarily software timestamped, and a software-timestamped ptp4l
// disciplines CLOCK_REALTIME itself (linuxptp ptp4l.8, and the identical
// case [ManagedProvider.Now] already serves). A PHCDevice declared
// against such an interface is a misconfiguration — the device this
// provider was told to trust does not exist here — and is refused rather
// than silently ignored.
func (p *ExternalProvider) Now(context.Context) MediaTime {
	index, hasPHC, err := phcIndexForInterface(p.cfg.Interface)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("cannot tell whether %s has a PHC, so which clock the observed ptp4l disciplines is unknown: %v", p.cfg.Interface, err)}
	}
	if !hasPHC {
		if p.cfg.PHCDevice != "" {
			return MediaTime{Valid: false, Reason: fmt.Sprintf("phcDevice %s is declared for %s, but %s has no PHC at all (ETHTOOL_GET_TS_INFO reports none): remove phcDevice or fix the declaration", p.cfg.PHCDevice, p.cfg.Interface, p.cfg.Interface)}
		}
		return MediaTime{
			Time:   time.Now(),
			Valid:  true,
			Reason: fmt.Sprintf("%s has no PHC, so the observed ptp4l is necessarily software timestamped and disciplines CLOCK_REALTIME directly", p.cfg.Interface),
		}
	}
	if p.cfg.PHCDevice == "" {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("%s has a PHC, and this provider cannot tell whether the ptp4l it observes reached hardware timestamping; wire a PHC device explicitly for media time", p.cfg.Interface)}
	}
	declaredIndex, ok := phcDeviceIndex(p.cfg.PHCDevice)
	if !ok {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("phcDevice %q does not name a PTP hardware clock device (want a form like \"/dev/ptp0\")", p.cfg.PHCDevice)}
	}
	if declaredIndex != index {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("phcDevice %s names PTP clock index %d, but %s's own PHC is /dev/ptp%d: fix phcDevice or the interface", p.cfg.PHCDevice, declaredIndex, p.cfg.Interface, index)}
	}
	t, err := readPHC(index)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("reading %s: %v (distributions ship this device root:root 0600; this agent may need a udev rule or group membership)", p.cfg.PHCDevice, err)}
	}
	return MediaTime{
		Time:   t,
		Valid:  true,
		Reason: fmt.Sprintf("media time from the operator-declared PHC %s on %s: this is an attested reading, never a verified hardware-timestamping read", p.cfg.PHCDevice, p.cfg.Interface),
	}
}

// phcDeviceIndex parses "/dev/ptpN" into N. ok is false for anything
// else, including a value [config.DecodeNodeClockPayload]'s own pattern
// should already have refused before this ever reaches an agent — this
// provider re-checks rather than trusting the wire, since a malformed
// declaration must be an honest refusal, never a panic or a wrong index.
func phcDeviceIndex(device string) (int, bool) {
	const prefix = "/dev/ptp"
	if !strings.HasPrefix(device, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(device, prefix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Poll reads TIME_STATUS_NP, PORT_DATA_SET, TIME_PROPERTIES_DATA_SET, and
// DEFAULT_DATA_SET off the read-only UDS socket via pmc. A socket that
// does not exist is reported as unreachable (RES-019 section 9's
// "interface or link loss is failed" — an externally-managed ptp4l that
// has never started, or that stopped, looks identical to this provider:
// no socket, no evidence).
func (p *ExternalProvider) Poll(ctx context.Context) RawStatus {
	if _, err := os.Stat(p.cfg.UDSAddress); err != nil {
		return RawStatus{Reachable: false, Reason: fmt.Sprintf("read-only management socket %s: %v", p.cfg.UDSAddress, err)}
	}
	return pollViaUDS(ctx, p.cfg.UDSAddress, p.cfg.Domain, "external (unidentified)", p.cfg.LocalSocketDir)
}

// pollViaUDS is [ExternalProvider.Poll]'s and [ManagedProvider.Poll]'s
// shared implementation: both read the SAME three management sets off a
// UDS socket via pmc, differing only in which socket, whether they also
// supervise the process behind it, what they report as Owner, and which
// directory (if any) they can already vouch for as writable for pmc's own
// local socket (socketDirHint, see [pmcLocalSocketDirs]).
func pollViaUDS(ctx context.Context, uds string, domain int, owner string, socketDirHint string) RawStatus {
	portOut, portErr := runPMC(ctx, uds, domain, "PORT_DATA_SET", socketDirHint)
	if portErr != nil {
		if pmcUnavailable(portErr) {
			// pmc itself never reached ptp4l (its own client-side setup
			// failed) — not RES-019 section 9's "interface or link loss"
			// or "ptp4l gone", so this must not read as StateFailed. Report
			// no lock evidence instead: Tracker settles into acquiring (or
			// holdover, if previously locked) exactly as it does for any
			// other reading with no proof of sync yet.
			return RawStatus{Reachable: true, Timescale: TimescaleUnknown, Owner: owner,
				Reason: fmt.Sprintf("clock state unknown: %v (this node's own pmc tooling failed, not evidence ptp4l is down)", portErr)}
		}
		return RawStatus{Reachable: false, Reason: portErr.Error()}
	}
	port := parsePortDataSet(portOut)
	if !port.PortStateKnown {
		return RawStatus{Reachable: false, Reason: "no response from ptp4l's management socket (wrong domain, or ptp4l is not actually running behind this socket)"}
	}

	tsOut, _ := runPMC(ctx, uds, domain, "TIME_STATUS_NP", socketDirHint)
	ts := parseTimeStatusNP(tsOut)

	propsOut, _ := runPMC(ctx, uds, domain, "TIME_PROPERTIES_DATA_SET", socketDirHint)
	props := parseTimePropertiesDataSet(propsOut)

	defOut, _ := runPMC(ctx, uds, domain, "DEFAULT_DATA_SET", socketDirHint)
	def := parseDefaultDataSet(defOut)

	raw := RawStatus{
		Reachable: true,
		Locked:    portStateLocked(port.PortState),
		Owner:     owner,
		Timescale: TimescaleUnknown,
	}
	if !raw.Locked {
		raw.Reason = fmt.Sprintf("port state is %s, not yet synchronized", port.PortState)
	}
	if role, ok := portStateToRole(port.PortState); ok {
		raw.Role, raw.RoleKnown = role, true
	}
	if def.DomainNumberKnown {
		raw.Domain, raw.DomainKnown = def.DomainNumber, true
	}
	if def.ClockClassKnown {
		raw.ClockClass, raw.ClockClassKnown = def.ClockClass, true
	}
	if ts.GMIdentityKnown {
		raw.GrandmasterIdentity, raw.GMKnown = ts.GMIdentity, true
	}
	if ts.MasterOffsetKnown {
		raw.OffsetNs, raw.OffsetKnown = ts.MasterOffsetNs, true
	}
	if props.PTPTimescaleKnown {
		if props.PTPTimescale {
			raw.Timescale = TimescalePTP
		} else {
			raw.Timescale = TimescaleArb
		}
	}
	return raw
}
