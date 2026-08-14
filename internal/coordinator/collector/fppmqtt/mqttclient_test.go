package fppmqtt

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestDefaultTopicPrefixMatchesConfigDefault proves the claim
// DefaultTopicPrefix's doc comment makes: internal/coordinator/config's
// own default for SHOWMESH_FPP_MQTT_TOPIC_PREFIX (applied whenever that
// variable is unset — see config.LoadConfigFrom) is byte-for-byte the same
// string as this package's DefaultTopicPrefix (applied whenever
// Options.TopicPrefix is empty — see New). The two are independently
// declared constants in different packages specifically so this package
// does not have to import internal/coordinator/config for normal
// operation (see doc.go); this test is what keeps that independence from
// silently drifting into two different fallback prefixes.
func TestDefaultTopicPrefixMatchesConfigDefault(t *testing.T) {
	cfg, err := config.LoadConfigFrom(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("config.LoadConfigFrom(all unset) error = %v", err)
	}
	if cfg.FPPMQTTTopicPrefix != DefaultTopicPrefix {
		t.Errorf("internal/coordinator/config's default FPPMQTTTopicPrefix = %q, this package's DefaultTopicPrefix = %q — they must match",
			cfg.FPPMQTTTopicPrefix, DefaultTopicPrefix)
	}
}

// newCapturingLogger returns a *slog.Logger whose output lands in w, so a
// test can assert on log content without touching the real default
// logger.
func newCapturingLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}

func TestNewRejectsEmptyBrokerURL(t *testing.T) {
	_, err := New(Options{Hosts: map[string]string{"main": "fpp-player"}})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for an empty BrokerURL")
	}
}

func TestNewRejectsUnsupportedScheme(t *testing.T) {
	_, err := New(Options{BrokerURL: "http://mqtt.example.com:1883", Hosts: map[string]string{"main": "fpp-player"}})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for an unsupported scheme")
	}
}

func TestNewRejectsBrokerURLWithNoHost(t *testing.T) {
	_, err := New(Options{BrokerURL: "tcp://", Hosts: map[string]string{"main": "fpp-player"}})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for a broker URL with no host")
	}
}

func TestNewRejectsEmptyHosts(t *testing.T) {
	_, err := New(Options{BrokerURL: "tcp://mqtt.example.com:1883"})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for empty Hosts")
	}
}

// TestNewRejectsBrokerURLWithUserinfo is the Step 5 review finding 6
// regression test: a broker URL carrying embedded credentials
// (tcp://user:pass@host:1883) must be rejected at construction, and the
// error New returns must never contain the password — otherwise the
// credential reaches the coordinator's startup log the same way
// c.brokerURL reaches every connect-retry log line (mqttclient.go), a
// second leak door alongside the correctly-redacted
// SHOWMESH_FPP_MQTT_PASSWORD field.
//
// Before trusting this test, New's userinfo checks (both
// brokerURLUserinfoPattern and the parsed.User != nil check) were removed
// and confirmed to make this test fail — New succeeded and returned a
// *Collector whose brokerURL field carried the credential verbatim; see
// this package's Step 5 review-fix report for that verification.
func TestNewRejectsBrokerURLWithUserinfo(t *testing.T) {
	const secret = "supersecretpassword"
	_, err := New(Options{
		BrokerURL: "tcp://someuser:" + secret + "@mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "fpp-player"},
	})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for a BrokerURL carrying userinfo")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("New() error = %q, must never contain the embedded password", err.Error())
	}
	if strings.Contains(err.Error(), "someuser") {
		t.Errorf("New() error = %q, must never contain the embedded username either", err.Error())
	}
}

// TestNewRejectsBrokerURLWithEmptyUserinfo covers the "@host" shape with
// no username at all (still syntactically userinfo, per url.Parse) — the
// same rejection must apply even when there is technically nothing to
// leak yet, since the field is meant for Username/Password, not the URL,
// unconditionally.
func TestNewRejectsBrokerURLWithEmptyUserinfo(t *testing.T) {
	_, err := New(Options{
		BrokerURL: "tcp://@mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "fpp-player"},
	})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for a BrokerURL carrying an empty userinfo segment")
	}
}

// TestNewRejectsMalformedBrokerURLWithUserinfoWithoutLeaking covers the
// pre-url.Parse regex path directly: a BrokerURL that is BOTH malformed in
// some other way AND carries userinfo must still be rejected without the
// credential ever reaching the returned error, even though a naive
// "wrap url.Parse's error" implementation would otherwise echo the raw
// input string (including the credential) via *url.Error's own Error()
// text.
func TestNewRejectsMalformedBrokerURLWithUserinfoWithoutLeaking(t *testing.T) {
	const secret = "supersecretpassword"
	_, err := New(Options{
		// A literal space is not valid in a URL host, so url.Parse itself
		// would fail on this string if it ever got that far.
		BrokerURL: "tcp://someuser:" + secret + "@bad host name:1883",
		Hosts:     map[string]string{"main": "fpp-player"},
	})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for a malformed BrokerURL carrying userinfo")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("New() error = %q, must never contain the embedded password even when the URL is otherwise malformed", err.Error())
	}
}

func TestNewRejectsInvalidInstanceID(t *testing.T) {
	_, err := New(Options{
		BrokerURL: "tcp://mqtt.example.com:1883",
		Hosts:     map[string]string{"Main_01": "fpp-player"},
	})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for an instance id that fails mqttproto.ValidateNodeID")
	}
}

func TestNewRejectsHostNameWithSlash(t *testing.T) {
	_, err := New(Options{
		BrokerURL: "tcp://mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "FPP/Main"},
	})
	if err == nil {
		t.Fatalf("New() error = nil, want an error for a HostName containing '/'")
	}
}

func TestNewRejectsHostNameWithWildcard(t *testing.T) {
	for _, bad := range []string{"FPP+Main", "FPP#Main", "FPP Main"} {
		_, err := New(Options{
			BrokerURL: "tcp://mqtt.example.com:1883",
			Hosts:     map[string]string{"main": bad},
		})
		if err == nil {
			t.Errorf("New() error = nil for HostName %q, want an error", bad)
		}
	}
}

func TestNewRejectsDuplicateHostName(t *testing.T) {
	_, err := New(Options{
		BrokerURL: "tcp://mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "fpp-player", "other": "fpp-player"},
	})
	if err == nil {
		t.Fatalf("New() error = nil, want an error when two instance ids map to the same HostName")
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "fpp-player"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.ID() != "fpp-mqtt" {
		t.Errorf("ID() = %q, want %q", c.ID(), "fpp-mqtt")
	}
	if c.topicPrefix != DefaultTopicPrefix {
		t.Errorf("topicPrefix = %q, want default %q", c.topicPrefix, DefaultTopicPrefix)
	}
	if c.PollInterval() != DefaultPollInterval {
		t.Errorf("PollInterval() = %v, want default %v", c.PollInterval(), DefaultPollInterval)
	}
}

func TestNewCustomTopicPrefixTrimmed(t *testing.T) {
	c, err := New(Options{
		BrokerURL:   "tcp://mqtt.example.com:1883",
		Hosts:       map[string]string{"main": "fpp-player"},
		TopicPrefix: "/custom/prefix/",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.topicPrefix != "custom/prefix" {
		t.Errorf("topicPrefix = %q, want %q (leading/trailing slashes trimmed)", c.topicPrefix, "custom/prefix")
	}
}

func TestNewCustomPollInterval(t *testing.T) {
	c, err := New(Options{
		BrokerURL:    "tcp://mqtt.example.com:1883",
		Hosts:        map[string]string{"main": "fpp-player"},
		PollInterval: 42 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.PollInterval() != 42*time.Second {
		t.Errorf("PollInterval() = %v, want %v", c.PollInterval(), 42*time.Second)
	}
}

// --- parseHostAndSuffix ------------------------------------------------

func TestParseHostAndSuffix(t *testing.T) {
	cases := []struct {
		prefix, topic string
		wantHost      string
		wantSuffix    string
		wantOK        bool
	}{
		{"falcon/player", "falcon/player/fpp-player/fppd_status", "fpp-player", "fppd_status", true},
		{"falcon/player", "falcon/player/fpp-player/playlist/repeat/status", "fpp-player", "playlist/repeat/status", true},
		{"falcon/player", "falcon/control/power", "", "", false},
		{"falcon/player", "falcon/player/fpp-player", "", "", false}, // no suffix at all
		{"falcon/player", "falcon/player/", "", "", false},
		{"falcon/player", "falcon/playerX/fpp-player/status", "", "", false}, // prefix must match a full segment
	}
	for _, tc := range cases {
		host, suffix, ok := parseHostAndSuffix(tc.prefix, tc.topic)
		if ok != tc.wantOK || host != tc.wantHost || suffix != tc.wantSuffix {
			t.Errorf("parseHostAndSuffix(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.prefix, tc.topic, host, suffix, ok, tc.wantHost, tc.wantSuffix, tc.wantOK)
		}
	}
}

// --- unmatched-host logging (contract section 4.4) ------------------------

// TestUnmatchedHostLoggedOnlyOnce proves the "logged at info once per
// host" half of contract section 4.4 directly, using a captured logger
// rather than reading stdout.
func TestUnmatchedHostLoggedOnlyOnce(t *testing.T) {
	var buf strings.Builder
	logger := newCapturingLogger(&buf)

	c, err := New(Options{
		BrokerURL: "tcp://mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "fpp-player"},
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	deliver(c, "falcon/player/FPP-Shed/status", []byte("idle"), false)
	deliver(c, "falcon/player/FPP-Shed/status", []byte("idle"), false)
	deliver(c, "falcon/player/FPP-Shed/status", []byte("idle"), false)

	count := strings.Count(buf.String(), "FPP-Shed")
	if count != 1 {
		t.Errorf("log mentions FPP-Shed %d times across 3 messages, want exactly 1 (logged once per host)", count)
	}
}

func TestUnmatchedHostLoggedSeparatelyPerHost(t *testing.T) {
	var buf strings.Builder
	logger := newCapturingLogger(&buf)

	c, err := New(Options{
		BrokerURL: "tcp://mqtt.example.com:1883",
		Hosts:     map[string]string{"main": "fpp-player"},
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	deliver(c, "falcon/player/FPP-Shed/status", []byte("idle"), false)
	deliver(c, "falcon/player/FPP-Garage/status", []byte("idle"), false)

	out := buf.String()
	if !strings.Contains(out, "FPP-Shed") {
		t.Errorf("log does not mention FPP-Shed at all")
	}
	if !strings.Contains(out, "FPP-Garage") {
		t.Errorf("log does not mention FPP-Garage at all")
	}
}
