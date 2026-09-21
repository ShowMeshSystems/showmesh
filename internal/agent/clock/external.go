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

	// PHCDevice is the operator-declared PTP hardware clock (e.g.
	// "/dev/ptp0") this provider's observed ptp4l keeps disciplined to PTP
	// time; see [ExternalProvider.Now] for why it must be declared.
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

// Now serves media time from a PHC-less interface (necessarily software
// timestamped) or from a PHC matching the operator-declared
// [ExternalConfig.PHCDevice]; every other case, including a mismatched
// PHCDevice, is refused. linuxptp's read-only management socket cannot
// report whether the observed ptp4l reached hardware timestamping
// (PORT_PROPERTIES_NP, the only management set carrying that field, is
// refused on any socket but ptp4l's own read-write one), so PHCDevice is
// the operator's attestation this provider relies on instead.
func (p *ExternalProvider) Now(context.Context) MediaTime {
	index, hasPHC, err := phcIndexForInterface(p.cfg.Interface)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("Could not check %s for a hardware clock: %v. Media time is unavailable until this is fixed.", p.cfg.Interface, err)}
	}
	if !hasPHC {
		if p.cfg.PHCDevice != "" {
			return MediaTime{Valid: false, Reason: fmt.Sprintf("phcDevice %s is set, but %s has no hardware clock. Remove phcDevice or point it at the right interface.", p.cfg.PHCDevice, p.cfg.Interface)}
		}
		return MediaTime{
			Time:   time.Now(),
			Valid:  true,
			Reason: fmt.Sprintf("%s has no hardware clock, so media time comes from the system clock.", p.cfg.Interface),
		}
	}
	if p.cfg.PHCDevice == "" {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("%s has a hardware clock, but this node cannot tell if it is being used. Set phcDevice to enable media time.", p.cfg.Interface)}
	}
	declaredIndex, ok := phcDeviceIndex(p.cfg.PHCDevice)
	if !ok {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("phcDevice %q is not a valid hardware clock device. Use a path like /dev/ptp0.", p.cfg.PHCDevice)}
	}
	if declaredIndex != index {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("phcDevice %s does not match %s's hardware clock, which is /dev/ptp%d. Fix phcDevice or the interface.", p.cfg.PHCDevice, p.cfg.Interface, index)}
	}
	t, err := readPHC(index)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("Could not read %s: %v. Check the device permissions.", p.cfg.PHCDevice, err)}
	}
	return MediaTime{
		Time:   t,
		Valid:  true,
		Reason: fmt.Sprintf("Media time is from the hardware clock %s on %s.", p.cfg.PHCDevice, p.cfg.Interface),
	}
}

// frequencyPPM mirrors [ExternalProvider.Now]'s own trust decision: an
// interface with no PHC is CLOCK_REALTIME, a declared and matching
// [ExternalConfig.PHCDevice] is that PHC. reason states why whenever ok
// is false.
func (p *ExternalProvider) frequencyPPM() (ppm float64, ok bool, reason string) {
	index, hasPHC, err := phcIndexForInterface(p.cfg.Interface)
	if err != nil {
		return 0, false, fmt.Sprintf("Could not check %s for a hardware clock: %v.", p.cfg.Interface, err)
	}
	if !hasPHC {
		v, err := realtimeFrequencyPPM()
		if err != nil {
			return 0, false, fmt.Sprintf("Could not read the system clock's frequency: %v.", err)
		}
		return v, true, ""
	}
	if p.cfg.PHCDevice == "" {
		return 0, false, fmt.Sprintf("%s has a hardware clock, but no PHC device is declared for it. Set phcDevice in this node's clock settings to report its frequency.", p.cfg.Interface)
	}
	declaredIndex, parsed := phcDeviceIndex(p.cfg.PHCDevice)
	if !parsed || declaredIndex != index {
		return 0, false, fmt.Sprintf("phcDevice does not match %s's hardware clock. Fix phcDevice in this node's clock settings.", p.cfg.Interface)
	}
	v, err := phcFrequencyPPM(index)
	if err != nil {
		return 0, false, fmt.Sprintf("Could not read /dev/ptp%d's frequency: %v.", index, err)
	}
	return v, true, ""
}

// phcDeviceIndex parses "/dev/ptpN" into N; ok is false for anything else.
// Re-checks rather than trusting the wire: a malformed value must be an
// honest refusal, never a panic or a wrong index.
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
		return RawStatus{Reachable: false, Reason: fmt.Sprintf("Cannot reach the clock socket %s: %v.", p.cfg.UDSAddress, err)}
	}
	raw := pollViaUDS(ctx, p.cfg.UDSAddress, p.cfg.Domain, "external (unidentified)", p.cfg.LocalSocketDir)
	if raw.Reachable {
		if ppm, ok, reason := p.frequencyPPM(); ok {
			raw.FrequencyPPM, raw.FrequencyPPMKnown = ppm, true
		} else {
			raw.FrequencyPPMReason = reason
		}
	}
	return raw
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
				Reason: fmt.Sprintf("Clock state is unknown: %v. This does not mean the clock is down.", portErr)}
		}
		return RawStatus{Reachable: false, Reason: portErr.Error()}
	}
	port := parsePortDataSet(portOut)
	if !port.PortStateKnown {
		return RawStatus{Reachable: false, Reason: "No response from the clock socket. Check the domain number, or confirm the clock service is running."}
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
