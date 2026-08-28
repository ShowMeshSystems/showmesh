package clock

import (
	"context"
	"fmt"
	"os"
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

// Now reports MediaTime.Valid=false: an external provider observes ptp4l
// state only (RES-019 section 5.3 scopes it to "observes only"). Reading
// the PHC device itself, when one is configured, is [ReadPHC] — a
// separate concern from which component owns the PTP protocol traffic,
// and this provider does not assume it also owns PHC access.
func (p *ExternalProvider) Now(context.Context) MediaTime {
	return MediaTime{Valid: false, Reason: "external provider observes PTP status only; wire a PHC device separately for media time"}
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
