package clock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// pmcBinary is the linuxptp management client this package shells out to
// for the external and managed providers' status reads (RES-019: "State
// from pmc GET on the read-only UDS socket"). A full path, not a bare
// name: Debian's linuxptp package installs it under /usr/sbin, which is
// not on every non-interactive shell's PATH (this seam's own bench
// discovered exactly that), and exec.LookPath("pmc") would silently miss
// it in that shell.
var pmcBinary = "/usr/sbin/pmc"

// pmcTimeout bounds one pmc invocation.
const pmcTimeout = 3 * time.Second

// errPMCUnavailable marks an error as pmc never having reached the target
// ptp4l instance at all: pmc's own client-side setup (starting the
// process, or binding the local -i socket [runPMC] gives it) failed
// before any request went out. Never evidence about the OBSERVED ptp4l's
// health — callers must not fold this into [RawStatus.Reachable]=false,
// which RES-019 section 9 reserves for interface/link loss and the owning
// ptp4l process being gone.
type errPMCUnavailable struct{ err error }

func (e *errPMCUnavailable) Error() string { return e.err.Error() }
func (e *errPMCUnavailable) Unwrap() error { return e.err }

// pmcUnavailable reports whether err came from pmc itself, i.e. this
// package's own tooling, rather than from ptp4l declining or ignoring the
// request.
func pmcUnavailable(err error) bool {
	var u *errPMCUnavailable
	return errors.As(err, &u)
}

// pmcLocalSocketMarkers are pmc's own diagnostic strings for a failure
// setting up its local client-side transport (the -i socket, or the
// broader "pmc" process itself), captured verbatim from a real failure
// against a non-writable default path: "pmc: uds: bind failed: Permission
// denied" / "pmc: failed to open transport" / "failed to create pmc".
var pmcLocalSocketMarkers = []string{"bind failed", "failed to open transport", "failed to create pmc"}

// runPMC shells out to pmc against uds, targeting domain (a management
// message whose domainNumber does not match the target ptp4l instance's
// own configured domain is silently dropped — discovered running the real
// binary against this seam's own bench, not documented anywhere pmc -h
// prints), and returns its raw stdout. managementID is one of pmc's
// management set names, e.g. "TIME_STATUS_NP".
//
// pmc binds its OWN client socket locally and, given no -i, hardcodes
// /var/run/pmc.$pid — unwritable to a non-root agent (/var/run is
// root:root 0755), so every GET fails with "uds: bind failed: Permission
// denied" no matter how healthy the observed ptp4l is. [pmcLocalSocket]
// gives pmc a path this process can certainly write, unique per
// invocation so concurrent reads cannot collide, removed here even when
// the read fails.
func runPMC(ctx context.Context, uds string, domain int, managementID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pmcTimeout)
	defer cancel()

	localSocket, err := pmcLocalSocket()
	if err != nil {
		return "", &errPMCUnavailable{fmt.Errorf("cannot allocate pmc's own local socket path: %w", err)}
	}
	defer func() { _ = os.Remove(localSocket) }()

	cmd := exec.CommandContext(ctx, pmcBinary, "-u", "-b", "0", "-i", localSocket, "-d", strconv.Itoa(domain), "-s", uds, "GET "+managementID)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			wrapped := fmt.Errorf("pmc GET %s against %s: %w: %s", managementID, uds, err, stderr)
			if pmcStderrIsLocalFailure(stderr) {
				return "", &errPMCUnavailable{wrapped}
			}
			return "", wrapped
		}
		// cmd.Output failed to even start pmc (binary missing, not
		// executable): certainly this package's own tooling, never
		// evidence about the observed ptp4l.
		return "", &errPMCUnavailable{fmt.Errorf("pmc GET %s against %s: %w", managementID, uds, err)}
	}
	return string(out), nil
}

// pmcLocalSocket reserves a unique path under the OS temp directory
// (world-writable, unlike /var/run) for pmc's own -i client socket, then
// removes the reservation so pmc can bind fresh: bind() fails if a
// filesystem entry already exists at the path.
func pmcLocalSocket() (string, error) {
	f, err := os.CreateTemp("", "pmc.*.sock")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func pmcStderrIsLocalFailure(stderr string) bool {
	for _, marker := range pmcLocalSocketMarkers {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

// pmcFields parses one pmc RESPONSE MANAGEMENT block's body into a
// name->value map: every line after the "RESPONSE MANAGEMENT <name>"
// header is "\t\t<field><whitespace><value>", where value is everything
// after the first run of whitespace, trimmed (so a value like "1
// 127.0.0.1" for protocolAddress round-trips whole). A management set pmc
// never got a response for (ptp4l down, wrong domain, a query the running
// version does not support) yields an empty map, not an error — the
// caller decides what "no data" means for the field it was asking for.
func pmcFields(output string) map[string]string {
	fields := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == line {
			// A field line is always indented; "sending: ..." and the
			// "<id>-<port> seq <n> RESPONSE MANAGEMENT <name>" header
			// line are not.
			continue
		}
		if !strings.Contains(trimmed, "RESPONSE MANAGEMENT") && trimmed != "" {
			parts := strings.SplitN(trimmed, " ", 2)
			if len(parts) != 2 {
				// A field with no value at all (should not happen against
				// a real pmc build) is skipped rather than recorded as an
				// empty string that would misrepresent "field absent" and
				// "field present but empty" identically.
				continue
			}
			name := parts[0]
			value := strings.TrimSpace(parts[1])
			fields[name] = value
		}
	}
	return fields
}

// pmcInt parses fields[name] as a base-10 integer, or the base-16 form
// pmc prints for a handful of fields (e.g. clockAccuracy "0xfe",
// timeSource "0xa0") when it starts with "0x". ok is false when the field
// is absent or unparseable.
func pmcInt(fields map[string]string, name string) (v int64, ok bool) {
	raw, present := fields[name]
	if !present {
		return 0, false
	}
	if strings.HasPrefix(raw, "0x") {
		n, err := strconv.ParseInt(strings.TrimPrefix(raw, "0x"), 16, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func pmcString(fields map[string]string, name string) (v string, ok bool) {
	raw, present := fields[name]
	if !present || raw == "" {
		return "", false
	}
	return raw, true
}

func pmcBool(fields map[string]string, name string) (v bool, ok bool) {
	raw, present := fields[name]
	if !present {
		return false, false
	}
	switch raw {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

// timeStatusNP is the fields of pmc's TIME_STATUS_NP management set this
// package reads (RES-019 section 5.2): master_offset, gmPresent,
// gmIdentity.
type timeStatusNP struct {
	MasterOffsetNs    int64
	MasterOffsetKnown bool

	GMPresent      bool
	GMPresentKnown bool

	GMIdentity      string
	GMIdentityKnown bool
}

func parseTimeStatusNP(output string) timeStatusNP {
	fields := pmcFields(output)
	var s timeStatusNP
	s.MasterOffsetNs, s.MasterOffsetKnown = pmcInt(fields, "master_offset")
	s.GMPresent, s.GMPresentKnown = pmcBool(fields, "gmPresent")
	s.GMIdentity, s.GMIdentityKnown = pmcString(fields, "gmIdentity")
	return s
}

// portDataSet is the fields of pmc's PORT_DATA_SET management set this
// package reads: portState.
type portDataSet struct {
	PortState      string
	PortStateKnown bool
}

func parsePortDataSet(output string) portDataSet {
	fields := pmcFields(output)
	var s portDataSet
	s.PortState, s.PortStateKnown = pmcString(fields, "portState")
	return s
}

// timePropertiesDataSet is the fields of pmc's TIME_PROPERTIES_DATA_SET
// management set this package reads: ptpTimescale, currentUtcOffset,
// timeTraceable.
type timePropertiesDataSet struct {
	PTPTimescale      bool
	PTPTimescaleKnown bool
}

func parseTimePropertiesDataSet(output string) timePropertiesDataSet {
	fields := pmcFields(output)
	var s timePropertiesDataSet
	s.PTPTimescale, s.PTPTimescaleKnown = pmcBool(fields, "ptpTimescale")
	return s
}

// defaultDataSet is the fields of pmc's DEFAULT_DATA_SET management set
// this package reads: domainNumber, clockClass.
type defaultDataSet struct {
	DomainNumber      int
	DomainNumberKnown bool

	ClockClass      int
	ClockClassKnown bool
}

func parseDefaultDataSet(output string) defaultDataSet {
	fields := pmcFields(output)
	var s defaultDataSet
	if v, ok := pmcInt(fields, "domainNumber"); ok {
		s.DomainNumber, s.DomainNumberKnown = int(v), true
	}
	if v, ok := pmcInt(fields, "clockClass"); ok {
		s.ClockClass, s.ClockClassKnown = int(v), true
	}
	return s
}

// portStateToRole maps pmc's PORT_DATA_SET.portState to this package's
// Role vocabulary. ok is false for a portState this package has no Role
// mapping for (INITIALIZING, FAULTY, DISABLED, UNCALIBRATED, PRE_MASTER) —
// [RawStatus.RoleKnown] false in that case, never a guessed role.
func portStateToRole(portState string) (Role, bool) {
	switch portState {
	case "MASTER":
		return RoleGrandmaster, true
	case "SLAVE":
		return RoleFollower, true
	case "PASSIVE":
		return RolePassive, true
	case "LISTENING":
		return RoleListening, true
	default:
		return "", false
	}
}

// portStateLocked reports whether portState is itself evidence of a
// usable lock, matching [RawStatus.Locked]'s own doc comment: SLAVE (with
// gmPresent — see the external/managed providers' own callers) or MASTER
// (self-referentially, this node IS the domain's grandmaster).
func portStateLocked(portState string) bool {
	return portState == "MASTER" || portState == "SLAVE"
}
