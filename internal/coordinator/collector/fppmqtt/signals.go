package fppmqtt

import (
	"regexp"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// sourceName is [observation.Observation.Source] for every observation
// this package produces, and this Collector's [Collector.ID].
const sourceName = "fpp-mqtt"

// Signal IDs this collector produces.
//
// Every constant below whose fact is also collected by
// internal/coordinator/collector/fpp (the REST collector) MUST carry the
// byte-for-byte identical string value that package uses — contract
// section 4.3: "Signal IDs are deliberately identical to Seam A's for the
// same facts. That is what makes [the precedence rule] meaningful and
// testable." They are declared independently here rather than imported
// from the fpp package: at the time this package was built, the fpp
// package had not yet landed most of the Step 5 signals this list covers
// (see decode.go's package-level doc comment), so importing them was not
// possible for the ones that do not exist yet, and this file keeps the
// whole list in one place rather than importing some constants and
// hand-copying others. Fold-in should verify byte-for-byte equality
// against the fpp package's own constants (ideally by promoting this list
// to something both packages import, which is a decision for whoever owns
// both seams at fold-in, not for this package to make unilaterally) —
// tests/signal_id_match_test.go names every string this file must not
// silently drift from.
const (
	// Overlapping with the fpp REST collector's existing Step 3/4 signals
	// (internal/coordinator/collector/fpp.Signal*): the fact is the same,
	// so the signal ID is the same, on both sources.
	SignalMode              observation.SignalID = "fpp.mode"
	SignalPlaylistName      observation.SignalID = "fpp.playlist.name"
	SignalSequenceName      observation.SignalID = "fpp.sequence.name"
	SignalPositionSeconds   observation.SignalID = "fpp.position.seconds"
	SignalPositionRemaining observation.SignalID = "fpp.position.remaining.seconds"
	SignalMultiSyncEnabled  observation.SignalID = "fpp.multisync.enabled"
	SignalSchedulerStatus   observation.SignalID = "fpp.scheduler.status"
	SignalUptimeSeconds     observation.SignalID = "fpp.uptime.seconds"
	SignalVersion           observation.SignalID = "fpp.version"
	SignalBranch            observation.SignalID = "fpp.branch"
	SignalStatus            observation.SignalID = "fpp.status"

	// New Step 5 playback signals (contract section 3.1's table), overlapping
	// with the fpp REST collector's Step 5 additions.
	SignalSongName               observation.SignalID = "fpp.song.name"
	SignalPlaylistRepeatMode     observation.SignalID = "fpp.playlist.repeat_mode"
	SignalPlaylistIndex          observation.SignalID = "fpp.playlist.index"
	SignalPlaylistCount          observation.SignalID = "fpp.playlist.count"
	SignalPlaylistType           observation.SignalID = "fpp.playlist.type"
	SignalSchedulerEnabled       observation.SignalID = "fpp.scheduler.enabled"
	SignalSchedulerNextPlaylist  observation.SignalID = "fpp.scheduler.next_playlist"
	SignalSchedulerNextStartTime observation.SignalID = "fpp.scheduler.next_start_time"
	SignalMediaFilename          observation.SignalID = "fpp.media.filename"
	SignalPositionElapsedSeconds observation.SignalID = "fpp.position.elapsed.seconds"

	// New Step 5 controller/network health signals (contract section 3.1).
	SignalFppdState             observation.SignalID = "fpp.fppd.state"
	SignalPowerBad              observation.SignalID = "fpp.power.bad"
	SignalBridging              observation.SignalID = "fpp.bridging"
	SignalChannelInputsEnabled  observation.SignalID = "fpp.channel_inputs.enabled"
	SignalChannelOutputsEnabled observation.SignalID = "fpp.channel_outputs.enabled"
	SignalUUID                  observation.SignalID = "fpp.uuid"
	SignalHostName              observation.SignalID = "fpp.host_name"
	SignalVolume                observation.SignalID = "fpp.volume"
	SignalMQTTConfigured        observation.SignalID = "fpp.mqtt.configured"
	SignalMQTTConnected         observation.SignalID = "fpp.mqtt.connected"
	SignalWarningsCount         observation.SignalID = "fpp.warnings.count"
	SignalWarningsSummary       observation.SignalID = "fpp.warnings.summary"

	// Port signals (contract section 3.1). Per-port and per-sensor signal
	// IDs (fpp.port.<key>.* and fpp.sensor.<key>.*) are built dynamically
	// by portSignalKind/portSignalCurrentMA/etc. below and
	// sensorSignalValue/sensorSignalType in decode.go, from a key derived
	// from the device's own reported name — see normalizeKey.
	SignalPortsCount        observation.SignalID = "fpp.ports.count"
	SignalPortsBlindCount   observation.SignalID = "fpp.ports.blind_count"
	SignalPortsDecodeFailed observation.SignalID = "fpp.ports.decode_failed"

	// SignalReady is MQTT-only: FPP's "ready" topic (1/0) has no REST
	// equivalent in the Step 3/4/5 contract, so this ID is deliberately
	// NOT shared with the fpp-rest source.
	SignalReady observation.SignalID = "fpp.ready"
)

// init validates every signal ID constant this package declares against
// [observation.ValidateSignalID], the same discipline contract section 3.1
// asks of the fpp REST collector: a malformed signal ID fails at package
// load, not at the first poll. allStaticSignalIDs (topics.go) covers every
// constant above except SignalPortsDecodeFailed, which is not any
// topicSpec's staticSignals member (it only ever appears conditionally,
// when portSignals finds a problem) — added explicitly here so this check
// still covers every constant this file declares.
func init() {
	for _, sig := range allStaticSignalIDs {
		if err := observation.ValidateSignalID(sig); err != nil {
			panic("fppmqtt: invalid signal ID constant: " + err.Error())
		}
	}
	if err := observation.ValidateSignalID(SignalPortsDecodeFailed); err != nil {
		panic("fppmqtt: invalid signal ID constant: " + err.Error())
	}
}

// keyNormalizeRunPattern matches any run of characters outside [a-z0-9],
// after lowercasing — the mechanism behind [normalizeKey].
var keyNormalizeRunPattern = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeKey implements contract sections 3.1 and 3.5's identical key
// rule for both a port's "name" field and a sensor's "label" field:
// lowercase, every run of characters outside [a-z0-9] collapsed to a
// single '_', leading and trailing '_' trimmed. "Port 1" becomes "port_1";
// "K16-Max: " (trailing colon and space, as FPP sends it) becomes
// "k16_max"; "CPU: " becomes "cpu".
func normalizeKey(raw string) string {
	lower := strings.ToLower(raw)
	collapsed := keyNormalizeRunPattern.ReplaceAllString(lower, "_")
	return strings.Trim(collapsed, "_")
}

// Per-port dynamic signal ID builders (contract section 3.1's port table).
// <key> is normalizeKey's output for that port's "name" field.
func portSignalKind(key string) observation.SignalID {
	return observation.SignalID("fpp.port." + key + ".kind")
}
func portSignalCurrentMA(key string) observation.SignalID {
	return observation.SignalID("fpp.port." + key + ".current_ma")
}
func portSignalEnabled(key string) observation.SignalID {
	return observation.SignalID("fpp.port." + key + ".enabled")
}
func portSignalStatus(key string) observation.SignalID {
	return observation.SignalID("fpp.port." + key + ".status")
}
func portSignalBank(key string) observation.SignalID {
	return observation.SignalID("fpp.port." + key + ".bank")
}
func portSignalPixelCount(key string) observation.SignalID {
	return observation.SignalID("fpp.port." + key + ".pixel_count")
}

// Per-sensor dynamic signal ID builders (contract section 3.1's sensor
// rows). <key> is normalizeKey's output for that sensor's "label" field.
func sensorSignalValue(key string) observation.SignalID {
	return observation.SignalID("fpp.sensor." + key + ".value")
}
func sensorSignalType(key string) observation.SignalID {
	return observation.SignalID("fpp.sensor." + key + ".type")
}
