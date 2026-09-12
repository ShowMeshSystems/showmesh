package clock

import (
	"context"
	"fmt"
	"os"
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

// Now serves media time only in the one case this provider can establish
// from evidence: an interface with no PHC at all.
//
// RES-019 section 5.3 gives this provider two time sources, the PHC in
// hardware timestamping mode and "the disciplined system clock in
// software mode". Which of the two applies depends on the timestamping
// mode the OBSERVED ptp4l actually reached, and nothing on the read-only
// management socket reports that mode, which is why the hardware case
// stays refused here: an interface that HAS a PHC may still be running an
// externally-owned ptp4l that fell back to software timestamping, and a
// provider that read that undisciplined PHC would be confidently wrong.
// Reading a PHC device is [ReadPHC], a separate concern from which
// component owns the PTP protocol traffic, and this provider does not
// assume it also owns PHC access.
//
// An interface with NO PHC settles the question: ptp4l cannot reach
// hardware timestamping without one, so the instance being observed is
// necessarily software timestamped, and a software-timestamped ptp4l
// disciplines CLOCK_REALTIME itself (linuxptp ptp4l.8, and the identical
// case [ManagedProvider.Now] already serves). The reading carries that
// reasoning as its own Reason, so a consumer can see what it rests on.
func (p *ExternalProvider) Now(context.Context) MediaTime {
	_, hasPHC, err := PHCIndexForInterface(p.cfg.Interface)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("cannot tell whether %s has a PHC, so which clock the observed ptp4l disciplines is unknown: %v", p.cfg.Interface, err)}
	}
	if hasPHC {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("%s has a PHC, and this provider cannot tell whether the ptp4l it observes reached hardware timestamping; wire a PHC device explicitly for media time", p.cfg.Interface)}
	}
	return MediaTime{
		Time:   time.Now(),
		Valid:  true,
		Reason: fmt.Sprintf("%s has no PHC, so the observed ptp4l is necessarily software timestamped and disciplines CLOCK_REALTIME directly", p.cfg.Interface),
	}
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
	return pollViaUDS(ctx, p.cfg.UDSAddress, p.cfg.Domain, "external (unidentified)")
}

// pollViaUDS is [ExternalProvider.Poll]'s and [ManagedProvider.Poll]'s
// shared implementation: both read the SAME three management sets off a
// UDS socket via pmc, differing only in which socket, whether they also
// supervise the process behind it, and what they report as Owner.
func pollViaUDS(ctx context.Context, uds string, domain int, owner string) RawStatus {
	portOut, portErr := runPMC(ctx, uds, domain, "PORT_DATA_SET")
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

	tsOut, _ := runPMC(ctx, uds, domain, "TIME_STATUS_NP")
	ts := parseTimeStatusNP(tsOut)

	propsOut, _ := runPMC(ctx, uds, domain, "TIME_PROPERTIES_DATA_SET")
	props := parseTimePropertiesDataSet(propsOut)

	defOut, _ := runPMC(ctx, uds, domain, "DEFAULT_DATA_SET")
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
