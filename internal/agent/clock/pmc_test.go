package clock

import (
	"os"
	"testing"
)

// readFixture loads a captured pmc output file from testdata/pmc — see
// that directory's README.md for provenance (real captures vs. hand-edited
// derivatives of real captures, and why some had to be derived).
func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/pmc/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseTimeStatusNPMaster(t *testing.T) {
	out := readFixture(t, "time_status_np.master.txt")
	s := parseTimeStatusNP(out)
	if !s.MasterOffsetKnown || s.MasterOffsetNs != 0 {
		t.Fatalf("master_offset = %d/%v, want 0/known", s.MasterOffsetNs, s.MasterOffsetKnown)
	}
	if !s.GMPresentKnown || s.GMPresent != false {
		t.Fatalf("gmPresent = %v/%v, want false/known", s.GMPresent, s.GMPresentKnown)
	}
	if !s.GMIdentityKnown || s.GMIdentity != "000000.fffe.000000" {
		t.Fatalf("gmIdentity = %q/%v, want 000000.fffe.000000/known", s.GMIdentity, s.GMIdentityKnown)
	}
}

func TestParseTimeStatusNPSlave(t *testing.T) {
	out := readFixture(t, "time_status_np.slave.txt")
	s := parseTimeStatusNP(out)
	if !s.MasterOffsetKnown || s.MasterOffsetNs != -42 {
		t.Fatalf("master_offset = %d/%v, want -42/known", s.MasterOffsetNs, s.MasterOffsetKnown)
	}
	if !s.GMPresentKnown || s.GMPresent != true {
		t.Fatalf("gmPresent = %v/%v, want true/known", s.GMPresent, s.GMPresentKnown)
	}
	if s.GMIdentity != "3cecef.fffe.a1b2c3" {
		t.Fatalf("gmIdentity = %q", s.GMIdentity)
	}
}

func TestParsePortDataSetMaster(t *testing.T) {
	out := readFixture(t, "port_data_set.master.txt")
	s := parsePortDataSet(out)
	if !s.PortStateKnown || s.PortState != "MASTER" {
		t.Fatalf("portState = %q/%v, want MASTER/known", s.PortState, s.PortStateKnown)
	}
}

func TestParsePortDataSetSlave(t *testing.T) {
	out := readFixture(t, "port_data_set.slave.txt")
	s := parsePortDataSet(out)
	if s.PortState != "SLAVE" {
		t.Fatalf("portState = %q, want SLAVE", s.PortState)
	}
}

func TestParsePortDataSetListening(t *testing.T) {
	out := readFixture(t, "port_data_set.listening.txt")
	s := parsePortDataSet(out)
	if s.PortState != "LISTENING" {
		t.Fatalf("portState = %q, want LISTENING", s.PortState)
	}
	if portStateLocked(s.PortState) {
		t.Fatalf("LISTENING must not be reported as locked")
	}
}

func TestParseTimePropertiesDataSetArb(t *testing.T) {
	out := readFixture(t, "time_properties_data_set.arb.txt")
	s := parseTimePropertiesDataSet(out)
	if !s.PTPTimescaleKnown || s.PTPTimescale != false {
		t.Fatalf("ptpTimescale = %v/%v, want false/known", s.PTPTimescale, s.PTPTimescaleKnown)
	}
}

func TestParseTimePropertiesDataSetPTP(t *testing.T) {
	out := readFixture(t, "time_properties_data_set.ptp.txt")
	s := parseTimePropertiesDataSet(out)
	if !s.PTPTimescaleKnown || s.PTPTimescale != true {
		t.Fatalf("ptpTimescale = %v/%v, want true/known", s.PTPTimescale, s.PTPTimescaleKnown)
	}
}

func TestParseDefaultDataSet(t *testing.T) {
	out := readFixture(t, "default_data_set.txt")
	s := parseDefaultDataSet(out)
	if !s.DomainNumberKnown || s.DomainNumber != 24 {
		t.Fatalf("domainNumber = %d/%v, want 24/known", s.DomainNumber, s.DomainNumberKnown)
	}
	if !s.ClockClassKnown || s.ClockClass != 248 {
		t.Fatalf("clockClass = %d/%v, want 248/known", s.ClockClass, s.ClockClassKnown)
	}
}

func TestParseAgainstEmptyOutputReportsUnknown(t *testing.T) {
	s := parseTimeStatusNP("")
	if s.MasterOffsetKnown || s.GMPresentKnown || s.GMIdentityKnown {
		t.Fatalf("empty pmc output must report every field unknown, got %+v", s)
	}
}

// TestParseAgainstOnlySendingLine covers the real shape pmc prints when a
// management request gets no response at all (wrong domain, ptp4l down,
// or a query the running version does not implement) — exactly this
// seam's own bench discovered running pmc against a live ptp4l with a
// mismatched -d: only "sending: GET <name>\n", no RESPONSE block.
func TestParseAgainstOnlySendingLine(t *testing.T) {
	s := parseTimeStatusNP("sending: GET TIME_STATUS_NP\n")
	if s.MasterOffsetKnown {
		t.Fatalf("a bare \"sending:\" line must not be read as evidence")
	}
}

func TestPortStateToRole(t *testing.T) {
	cases := map[string]Role{"MASTER": RoleGrandmaster, "SLAVE": RoleFollower, "PASSIVE": RolePassive, "LISTENING": RoleListening}
	for portState, want := range cases {
		got, ok := portStateToRole(portState)
		if !ok || got != want {
			t.Errorf("portStateToRole(%q) = %q/%v, want %q/true", portState, got, ok, want)
		}
	}
	if _, ok := portStateToRole("FAULTY"); ok {
		t.Errorf("portStateToRole(FAULTY) should report unknown, not a guessed role")
	}
}
