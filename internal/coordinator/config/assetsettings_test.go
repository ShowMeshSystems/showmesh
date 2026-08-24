package config

import (
	"strings"
	"testing"
	"time"
)

var testAssetSettings = AssetSettings{
	ContentBaseURL: "https://coordinator.example", MaxUploadBytes: 1 << 20,
	SyncInterval: 5 * time.Minute, InventoryInterval: 2 * time.Minute,
}

func TestEncodeAssetSettingsPayloadRoundTrips(t *testing.T) {
	raw, err := EncodeAssetSettingsPayload(testAssetSettings)
	if err != nil {
		t.Fatalf("EncodeAssetSettingsPayload() error = %v", err)
	}

	got, err := DecodeAssetSettingsPayload(raw)
	if err != nil {
		t.Fatalf("DecodeAssetSettingsPayload() error = %v", err)
	}
	if got != testAssetSettings {
		t.Errorf("round trip = %+v, want %+v", got, testAssetSettings)
	}
}

// TestEncodeAssetSettingsPayloadCarriesSecondsOnTheWire proves durations
// are encoded as plain integer seconds, matching this contract's existing
// "...Seconds" convention, not a Go duration string.
func TestEncodeAssetSettingsPayloadCarriesSecondsOnTheWire(t *testing.T) {
	raw, err := EncodeAssetSettingsPayload(AssetSettings{
		ContentBaseURL: "", MaxUploadBytes: 1, SyncInterval: 90 * time.Second, InventoryInterval: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("EncodeAssetSettingsPayload() error = %v", err)
	}
	want := `{"contentBaseUrl":"","maxUploadBytes":1,"syncIntervalSeconds":90,"inventoryIntervalSeconds":120}`
	if raw != want {
		t.Errorf("EncodeAssetSettingsPayload() = %q, want %q", raw, want)
	}
}

// TestEncodeAssetSettingsPayloadSurvivesSubSecondIntervals is a regression
// test for a real defect this seam's own acceptance run caught: an
// earlier version of the wire payload carried whole INTEGER seconds
// (int64(d / time.Second)), which truncates any interval under one second
// to zero. This project's own integration harness legitimately configures
// SHOWMESH_ASSET_SYNC_INTERVAL=750ms/SHOWMESH_ASSET_INVENTORY_INTERVAL=250ms
// to keep the suite fast, so the truncated value (0s) was then written to
// the store, and every later coordinator boot refused to start: it
// disagreed with the (correct, non-zero) environment value it was
// comparing against. FLOAT64 seconds fixes this by construction; this
// test is what stops a future edit from reintroducing the integer
// version.
func TestEncodeAssetSettingsPayloadSurvivesSubSecondIntervals(t *testing.T) {
	in := AssetSettings{
		ContentBaseURL: "", MaxUploadBytes: 1,
		SyncInterval: 750 * time.Millisecond, InventoryInterval: 250 * time.Millisecond,
	}
	raw, err := EncodeAssetSettingsPayload(in)
	if err != nil {
		t.Fatalf("EncodeAssetSettingsPayload() error = %v", err)
	}
	got, err := DecodeAssetSettingsPayload(raw)
	if err != nil {
		t.Fatalf("DecodeAssetSettingsPayload() error = %v", err)
	}
	if got.SyncInterval == 0 || got.InventoryInterval == 0 {
		t.Fatalf("round trip = %+v, want non-zero intervals (a sub-second interval must not truncate to zero)", got)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

func TestDecodeAssetSettingsPayloadRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeAssetSettingsPayload("not json"); err == nil {
		t.Fatalf("DecodeAssetSettingsPayload(%q) error = nil, want an error", "not json")
	}
}

func TestAssetSettingsEqualDetectsADifference(t *testing.T) {
	a := testAssetSettings
	b := testAssetSettings
	b.SyncInterval = time.Hour

	if AssetSettingsEqual(a, b) {
		t.Errorf("AssetSettingsEqual(%+v, %+v) = true, want false", a, b)
	}
	if !AssetSettingsEqual(a, a) {
		t.Errorf("AssetSettingsEqual(%+v, %+v) = false, want true", a, a)
	}
}

func TestDefaultAssetSettingsIsValid(t *testing.T) {
	if err := ValidateAssetSettings(DefaultAssetSettings()); err != nil {
		t.Errorf("ValidateAssetSettings(DefaultAssetSettings()) error = %v, want nil", err)
	}
	if DefaultAssetSettings().ContentBaseURL != "" {
		t.Errorf("DefaultAssetSettings().ContentBaseURL = %q, want empty (sync disabled by default)", DefaultAssetSettings().ContentBaseURL)
	}
}

func TestValidateAssetSettingsAcceptsEmptyContentBaseURL(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = ""
	if err := ValidateAssetSettings(s); err != nil {
		t.Errorf("ValidateAssetSettings() error = %v, want nil (empty contentBaseUrl is a valid, deliberate state)", err)
	}
}

func TestValidateAssetSettingsRejectsNonPositiveMaxUploadBytes(t *testing.T) {
	s := testAssetSettings
	s.MaxUploadBytes = 0
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for maxUploadBytes <= 0")
	}
}

func TestValidateAssetSettingsRejectsNonPositiveSyncInterval(t *testing.T) {
	s := testAssetSettings
	s.SyncInterval = 0
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for syncInterval <= 0")
	}
}

func TestValidateAssetSettingsRejectsNonPositiveInventoryInterval(t *testing.T) {
	s := testAssetSettings
	s.InventoryInterval = -time.Second
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for inventoryInterval <= 0")
	}
}

func TestValidateAssetSettingsRejectsMalformedURL(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = "not a url"
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for a malformed contentBaseUrl")
	}
}

func TestValidateAssetSettingsRejectsNonHTTPScheme(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = "ftp://coordinator.example"
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for a non-http(s) scheme")
	}
}

func TestValidateAssetSettingsRejectsUserinfo(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = "https://user:pass@coordinator.example"
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for a URL carrying userinfo")
	}
}

func TestValidateAssetSettingsRejectsMissingHost(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = "https:///path"
	if err := ValidateAssetSettings(s); err == nil {
		t.Error("ValidateAssetSettings() error = nil, want an error for a URL with no host")
	}
}

// TestContentBaseURLIsLoopback pins the exact set of hosts this predicate
// must catch (a node fetching from any of these fetches from itself, or
// from nothing at all) and the exact set it must not.
func TestContentBaseURLIsLoopback(t *testing.T) {
	loopback := []string{
		"http://localhost:8080",
		"http://LOCALHOST:8080",
		"http://127.0.0.1:8080",
		"http://127.0.0.5:8080",
		"http://[::1]:8080",
		"http://0.0.0.0:8080",
		"http://[::]:8080",
		"http://0x7f000001:8080",
		"http://2130706433:8080",
		"http://017700000001:8080",
		"http://127.1:8080",
		"http://127.000.000.001:8080",
		"http://0:8080",
		"http://localhost.:8080",
		"http://127.0.1:8080",
		"http://0x7f.0.0.1:8080",
		"http://127.0x1:8080",
		"http://0177.1:8080",
	}
	for _, u := range loopback {
		if !ContentBaseURLIsLoopback(u) {
			t.Errorf("ContentBaseURLIsLoopback(%q) = false, want true", u)
		}
	}

	routable := []string{
		"",
		"http://coordinator:8080",
		"http://coordinator.example.lan:8080",
		"http://192.168.1.50:8080",
		"not a url",
		"http://8.8.8.8:8080",
		"http://203.0.113.5:8080",
		"http://999.1:8080",
		"http://134744072:8080",
		"http://4294967295:8080",
		"http://0x100000000:8080",
		"http://0.0.0.256:8080",
		"http://1.2.3.4.5:8080",
		"http://08:8080",
		"http://0x:8080",
	}
	for _, u := range routable {
		if ContentBaseURLIsLoopback(u) {
			t.Errorf("ContentBaseURLIsLoopback(%q) = true, want false", u)
		}
	}
}

// TestValidateAssetSettingsRejectsLoopbackContentBaseURL pins the new
// refusal: a node fetching asset bytes from a loopback address fetches
// from itself, not from the coordinator, and this must be caught before it
// ever reaches a fleet.
func TestValidateAssetSettingsRejectsLoopbackContentBaseURL(t *testing.T) {
	for _, u := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://0.0.0.0:8080",
		"http://0x7f000001:8080",
		"http://2130706433:8080",
		"http://017700000001:8080",
		"http://127.1:8080",
		"http://127.000.000.001:8080",
		"http://0:8080",
	} {
		s := testAssetSettings
		s.ContentBaseURL = u
		if err := ValidateAssetSettings(s); err == nil {
			t.Errorf("ValidateAssetSettings() error = nil for contentBaseUrl %q, want an error", u)
		}
	}
}

// TestValidateAssetSettingsAcceptsRoutableContentBaseURL proves the new
// loopback check does not reject a legitimate, remotely-reachable value.
func TestValidateAssetSettingsAcceptsRoutableContentBaseURL(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = "http://coordinator:8080"
	if err := ValidateAssetSettings(s); err != nil {
		t.Errorf("ValidateAssetSettings() error = %v, want nil for a routable contentBaseUrl", err)
	}
}

// TestValidateAssetSettingsLoopbackErrorNamesTheShowmeshctlRemedyFirst pins
// commit e2d6c47's ordering convention (see fppmqtt_test.go's identical
// assertion for ValidateFPPMQTTHostIDs): the refusal must lead with the
// `showmeshctl assets settings set` remedy and name
// SHOWMESH_ASSET_CONTENT_BASE_URL only as a trailing hedge for a
// coordinator that has not migrated yet.
func TestValidateAssetSettingsLoopbackErrorNamesTheShowmeshctlRemedyFirst(t *testing.T) {
	s := testAssetSettings
	s.ContentBaseURL = "http://localhost:8080"
	err := ValidateAssetSettings(s)
	if err == nil {
		t.Fatal("ValidateAssetSettings() error = nil, want an error for a loopback contentBaseUrl")
	}
	got := err.Error()
	remedyIdx := strings.Index(got, "showmeshctl assets settings set")
	if remedyIdx < 0 {
		t.Fatalf("error = %q, want it to lead with the `showmeshctl assets settings set` remedy", got)
	}
	if idx := strings.Index(got, "SHOWMESH_ASSET_CONTENT_BASE_URL"); idx >= 0 && idx < remedyIdx {
		t.Errorf("error = %q, want the showmeshctl remedy to lead and SHOWMESH_ASSET_CONTENT_BASE_URL to appear only as a trailing hedge", got)
	}
}
