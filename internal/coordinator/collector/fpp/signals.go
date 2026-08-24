package fpp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SignalValue is one decoded signal, with no observation time attached: the
// functions in this file decode a response body into facts, never into
// [observation.Observation] values, and never touch a clock. That split is
// deliberate (contract section 3, "these decode ONLY"): Collector.Poll is
// the only place in this package that knows what time it is, and it is
// what turns a []SignalValue into observations, stamped with its own
// ObservedAt/CollectedAt. This is also the seam Seam B's MQTT collector was
// specified to decode against, so the same status/ports document produces
// identical signals regardless of whether it arrived over REST or MQTT.
//
// Value is one of bool, string, int64, float64 (matching
// [observation.Observation.Value]'s own accepted types), or nil when the
// signal is absent — Absence and Reason are then set instead, and Value,
// Unit are the zero value. A SignalValue is never both: exactly one of
// (Value set) or (Absence set) holds, mirroring
// [observation.Observation.Validate]'s own invariant one layer up.
type SignalValue struct {
	Signal  observation.SignalID
	Value   any
	Unit    string
	Absence observation.State
	Reason  string
}

// StatusSignals decodes an /api/fppd/status response body (REST) or an
// fppd_status MQTT payload (same document, per contract section 4.3) into
// every signal this package knows how to read from it: existing playback
// signals, Step 5's new playback/controller/network-health signals, and any
// sensor readings present. It returns an error only when body is not a
// JSON object at all (contract section "the error return is reserved
// for..."); a single absent or malformed field degrades only that field's
// SignalValue to an absence, never the whole call.
func StatusSignals(body []byte) ([]SignalValue, error) {
	doc, err := decodeRawDoc(body)
	if err != nil {
		return nil, err
	}

	// mode_name governs which of the playback signals below are genuinely
	// absent (contract section 3.3) rather than failed to collect. modeErr
	// is deliberately checked, not ignored: if this package cannot even
	// tell what mode the host is in, it must not guess that an absence is
	// mode-explained.
	modeName, modeErr := doc.stringField("mode_name")

	out := make([]SignalValue, 0, 40)

	out = append(out, stringSignalValue(doc, SignalVersion, "version"))
	out = append(out, stringSignalValue(doc, SignalMode, "mode_name"))
	out = append(out, stringSignalValue(doc, SignalStatus, "status_name"))
	out = append(out, sequenceNameSignal(doc))
	out = append(out, playlistNameSignal(doc))
	out = append(out, numberSignalValue(doc, SignalPositionSeconds, "seconds_played", "seconds"))
	out = append(out, numberSignalValue(doc, SignalPositionRemaining, "seconds_remaining", "seconds"))
	out = append(out, multiSyncEnabledSignalValue(doc))
	// "uptimeSeconds" is only the SECONDS component of FPP's
	// days/hours/minutes/seconds uptime breakdown (0-59, wrapping every
	// minute) — "uptimeTotalSeconds" is the actual total, true on every FPP
	// version this package has captured (9.4, 9.5.3) and confirmed true on
	// 10.0 as well. This collector previously read "uptimeSeconds" here
	// and published a value that wrapped every 60 seconds.
	out = append(out, numberSignalValue(doc, SignalUptimeSeconds, "uptimeTotalSeconds", "seconds"))
	out = append(out, stringSignalValue(doc, SignalSongName, "current_song"))

	// Playback signals whose absence is explained by the host being in
	// remote mode (contract section 3.3): all four are only ever present
	// on fpp-player's (player-mode) capture and entirely absent, not empty,
	// on both remote captures.
	out = append(out, modeGovernedTopLevelInt(doc, SignalPlaylistRepeatMode, "repeat_mode", "",
		modeName, modeErr, "player", "a repeat mode"))
	out = append(out, modeGovernedNestedInt(doc, SignalPlaylistIndex, "current_playlist", "index",
		modeName, modeErr, "player", "a playlist index"))
	out = append(out, modeGovernedNestedInt(doc, SignalPlaylistCount, "current_playlist", "count",
		modeName, modeErr, "player", "a playlist item count"))
	out = append(out, modeGovernedNestedString(doc, SignalPlaylistType, "current_playlist", "type",
		modeName, modeErr, "player", "a playlist type"))
	out = append(out, schedulerEnabledSignal(doc, modeName, modeErr))
	out = append(out, schedulerStatusSignalValue(doc, modeName, modeErr))

	nextName, nextNameErr, nextStart, nextStartErr := schedulerNextPlaylistFields(doc)
	out = append(out, modeGovernedStringResult(SignalSchedulerNextPlaylist, nextName, nextNameErr,
		modeName, modeErr, "player", "a scheduled next playlist"))
	out = append(out, modeGovernedIntResult(SignalSchedulerNextStartTime, nextStart, nextStartErr, "",
		modeName, modeErr, "player", "a scheduled next playlist"))

	// The inverse: present only in remote mode, per contract section 3.1's
	// "remote-mode only" notes, and absent (not merely empty) on fpp-player.
	out = append(out, modeGovernedTopLevelString(doc, SignalMediaFilename, "media_filename",
		modeName, modeErr, "remote", "a media filename"))
	out = append(out, modeGovernedTopLevelInt(doc, SignalPositionElapsedSeconds, "seconds_elapsed", "seconds",
		modeName, modeErr, "remote", "an elapsed-seconds counter"))
	out = append(out, elapsedMSSignal(doc))

	// Controller and network health.
	out = append(out, stringSignalValue(doc, SignalFPPDState, "fppd"))
	out = append(out, boolSignalValue(doc, SignalPowerBad, "powerBad"))
	out = append(out, boolSignalValue(doc, SignalBridging, "bridging"))
	out = append(out, boolSignalValue(doc, SignalChannelInputsEnabled, "channelInputsEnabled"))
	out = append(out, boolSignalValue(doc, SignalChannelOutputsEnabled, "channelOutputsEnabled"))
	out = append(out, stringSignalValue(doc, SignalBranch, "branch"))
	out = append(out, stringSignalValue(doc, SignalUUID, "uuid"))
	out = append(out, stringSignalValue(doc, SignalHostName, "host_name"))
	out = append(out, intSignalValue(doc, SignalVolume, "volume", ""))
	out = append(out, mqttNestedBoolSignal(doc, SignalMQTTConfigured, "configured"))
	out = append(out, mqttNestedBoolSignal(doc, SignalMQTTConnected, "connected"))

	out = append(out, warningsSignals(doc)...)
	out = append(out, sensorSignals(doc)...)

	return out, nil
}

// sequenceNameSignal resolves fpp.sequence.name from current_sequence when
// present, otherwise sequence_filename. Motivating capture:
// live_main_fppd_status.json (player mode) and both live_remote0*
// captures (remote mode) all carry current_sequence (empty when idle), so
// the fallback is not exercised by any capture this package has — it
// exists because sequence_filename is present only on the two remote
// captures, which suggests a real FPP configuration could plausibly report
// only one of the two, not because it has been observed.
func sequenceNameSignal(doc rawDoc) SignalValue {
	if v, err := doc.stringField("current_sequence"); err == nil {
		return SignalValue{Signal: SignalSequenceName, Value: v}
	}
	if v, err := doc.stringField("sequence_filename"); err == nil {
		return SignalValue{Signal: SignalSequenceName, Value: v}
	}
	return SignalValue{
		Signal:  SignalSequenceName,
		Absence: observation.StateCollectionFailed,
		Reason:  `neither "current_sequence" nor "sequence_filename" present in response`,
	}
}

// playlistNameSignal resolves fpp.playlist.name from
// current_playlist.playlist when present, otherwise the top-level
// "playlist" field. Motivating capture: live_main_fppd_status.json
// (player mode) carries current_playlist.playlist ("" when idle);
// live_remote01_fppd_status.json and live_remote04_fppd_status.json
// (remote mode) have no current_playlist object at all and instead carry a
// top-level "playlist" field, also "" when idle. Both are genuinely empty
// strings, never absences — see decode.go's stringField doc comment.
func playlistNameSignal(doc rawDoc) SignalValue {
	if v, err := doc.nestedStringField("current_playlist", "playlist"); err == nil {
		return SignalValue{Signal: SignalPlaylistName, Value: v}
	}
	if v, err := doc.stringField("playlist"); err == nil {
		return SignalValue{Signal: SignalPlaylistName, Value: v}
	}
	return SignalValue{
		Signal:  SignalPlaylistName,
		Absence: observation.StateCollectionFailed,
		Reason:  `neither "current_playlist.playlist" nor top-level "playlist" present in response`,
	}
}

// multiSyncEnabledSignalValue reads the top-level "multisync" boolean —
// the running daemon's actual behavior, never
// /api/settings/MultiSyncEnabled's schema. See doc.go's package comment
// for the trap this avoids. false is reported exactly like any other
// successfully-read value — a real, current value, never an absence: a
// disabled feature is a configuration fact stated positively, not a
// fault.
func multiSyncEnabledSignalValue(doc rawDoc) SignalValue {
	v, err := doc.boolField("multisync")
	if err != nil {
		return SignalValue{Signal: SignalMultiSyncEnabled, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: SignalMultiSyncEnabled, Value: v}
}

// schedulerEnabledSignal reads scheduler.enabled, tolerating the JSON
// NUMBER encoding every live capture actually uses (see
// decode.go's boolFromNumberOrBool), and is Unsupported rather than
// collection_failed when the whole "scheduler" object is absent because
// the host is in remote mode (contract section 3.3).
func schedulerEnabledSignal(doc rawDoc, modeName string, modeErr error) SignalValue {
	v, err := doc.nestedBoolFromNumberOrBool("scheduler", "enabled")
	return modeGovernedBoolResult(SignalSchedulerEnabled, v, err, modeName, modeErr, "player", "a scheduler")
}

// schedulerStatusSignalValue reads scheduler.status. This is the contract
// section 3.3 bug fix: previously this signal reported collection_failed
// unconditionally when "scheduler" was absent, which is wrong on a
// remote-mode host — nothing failed, remote mode simply does not have a
// scheduler.
func schedulerStatusSignalValue(doc rawDoc, modeName string, modeErr error) SignalValue {
	v, err := doc.nestedStringField("scheduler", "status")
	return modeGovernedStringResult(SignalSchedulerStatus, v, err, modeName, modeErr, "player", "a scheduler")
}

// schedulerNextPlaylistFields reads scheduler.nextPlaylist.playlistName and
// .scheduledStartTime in one pass, since both live under the same
// two-level-nested container and share the same "absent because remote
// mode" explanation. Each return value's error is independent, matching
// this package's per-field decoding discipline: a decode problem specific
// to one need not affect the other.
func schedulerNextPlaylistFields(doc rawDoc) (playlistName string, playlistNameErr error, startTime int64, startTimeErr error) {
	sched, err := doc.objectField("scheduler")
	if err != nil {
		return "", err, 0, err
	}
	next, err := sched.objectField("nextPlaylist")
	if err != nil {
		return "", err, 0, err
	}
	playlistName, playlistNameErr = next.stringField("playlistName")
	startTime, startTimeErr = next.intField("scheduledStartTime")
	return playlistName, playlistNameErr, startTime, startTimeErr
}

// mqttNestedBoolSignal reads a bool field nested under the top-level
// "MQTT" object (configured, connected). Both are genuine JSON booleans in
// every capture — see nestedBoolField.
func mqttNestedBoolSignal(doc rawDoc, sig observation.SignalID, innerKey string) SignalValue {
	v, err := doc.nestedBoolField("MQTT", innerKey)
	if err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: sig, Value: v}
}

// warningsSignals implements contract section 3.4. FPP's own source (see
// this package's doc comment for the citation) omits the "warnings" key
// from /api/fppd/status entirely when there are no active warnings, rather
// than publishing an empty array — confirmed live on
// live_remote04_fppd_status.json, which has no "warnings" key at all,
// against live_main_fppd_status.json and live_remote01_fppd_status.json,
// which both carry a populated array. Per contract section 3.4, REST
// absence is modeled as Unsupported, not collection_failed and not a
// fabricated zero: the MQTT "warnings" topic (Seam B) reports the same
// fact positively (an explicit "[]"), and section 5.2's precedence rule is
// what lets that positive answer win once both sources are wired in —
// this function does not and must not know that.
func warningsSignals(doc rawDoc) []SignalValue {
	const absentReason = `FPP omits the warnings key from /api/fppd/status; the MQTT warnings topic reports the list positively`

	raw, ok := doc["warnings"]
	if !ok {
		return []SignalValue{
			{Signal: SignalWarningsCount, Absence: observation.StateUnsupported, Reason: absentReason},
			{Signal: SignalWarningsSummary, Absence: observation.StateUnsupported, Reason: absentReason},
		}
	}

	// A present-but-null "warnings" key is not the same claim as an
	// absent key (contract section 3.4 distinguishes "absent" from
	// "positively empty"; JSON null is neither) and must not silently
	// decode to an empty list the way json.Unmarshal(null, &list) would —
	// see decode.go's isJSONNull doc comment for the general hazard this
	// closes. This is reachable from a real publisher: any client with
	// rights on the broker can put {"warnings":null,...} on
	// falcon/player/<host>/port_status's sibling fppd_status topic.
	if isJSONNull(raw) {
		reason := `field "warnings" is explicitly null`
		return []SignalValue{
			{Signal: SignalWarningsCount, Absence: observation.StateCollectionFailed, Reason: reason},
			{Signal: SignalWarningsSummary, Absence: observation.StateCollectionFailed, Reason: reason},
		}
	}

	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		reason := fmt.Sprintf(`field "warnings": expected an array of strings: %v`, err)
		return []SignalValue{
			{Signal: SignalWarningsCount, Absence: observation.StateCollectionFailed, Reason: reason},
			{Signal: SignalWarningsSummary, Absence: observation.StateCollectionFailed, Reason: reason},
		}
	}

	return []SignalValue{
		{Signal: SignalWarningsCount, Value: int64(len(list))},
		{Signal: SignalWarningsSummary, Value: strings.Join(list, "; ")},
	}
}

// sensorSignals decodes the "sensors" array into fpp.sensor.<key>.value and
// fpp.sensor.<key>.type pairs, per contract section 3.5. <key> is
// normalizeKey applied to the sensor's "label", which arrives with a
// trailing colon and space ("CPU: ", "K16-Max: " — see testdata) that
// normalizeKey strips along with everything else outside [a-z0-9].
//
// Deliberately does NOT claim a unit: valueType ("Temperature", "Voltage")
// says what kind of reading this is, never whether a temperature is
// Celsius or Fahrenheit, which FPP does not state and this package does
// not guess (contract section 3.5). fpp.sensor.<key>.value's Unit is
// always empty; the type is carried on the separate .type signal instead.
//
// A missing "sensors" key, or one that is not a JSON array, produces no
// signals at all rather than an absence: like per-port signals, the set of
// sensors an instance has cannot be known before it is actually observed,
// so this is a dynamic signal family in the same sense
// apiwiring.go's not-yet-polled placeholder synthesis already treats ports
// (contract section 5.4) — there is no aggregate "fpp.sensors.count" this
// step's table asks for to report an absence against. A sensor element
// that itself fails to decode (bad label, bad value, bad type) is skipped
// individually rather than failing the rest of the array, matching this
// package's per-field decoding discipline throughout.
func sensorSignals(doc rawDoc) []SignalValue {
	arr, err := doc.arrayField("sensors")
	if err != nil {
		return nil
	}

	var out []SignalValue
	seen := make(map[string]bool, len(arr))
	for _, raw := range arr {
		elem, err := decodeRawDoc(raw)
		if err != nil {
			continue
		}
		label, err := elem.stringField("label")
		if err != nil {
			continue
		}
		key := normalizeKey(label)
		if key == "" || seen[key] {
			// An unnamed sensor, or two sensors whose labels normalize to
			// the same key, cannot be told apart by <key> alone: skip
			// rather than guess or silently let the second overwrite the
			// first at read time.
			continue
		}
		seen[key] = true

		if v, err := elem.numberField("value"); err == nil {
			out = append(out, SignalValue{Signal: observation.SignalID("fpp.sensor." + key + ".value"), Value: v})
		}
		if v, err := elem.stringField("valueType"); err == nil {
			out = append(out, SignalValue{Signal: observation.SignalID("fpp.sensor." + key + ".type"), Value: v})
		}
	}
	return out
}

// --- generic field -> SignalValue helpers ----------------------------------

func stringSignalValue(doc rawDoc, sig observation.SignalID, key string) SignalValue {
	v, err := doc.stringField(key)
	if err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: sig, Value: v}
}

func boolSignalValue(doc rawDoc, sig observation.SignalID, key string) SignalValue {
	v, err := doc.boolField(key)
	if err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: sig, Value: v}
}

func numberSignalValue(doc rawDoc, sig observation.SignalID, key, unit string) SignalValue {
	v, err := doc.numberField(key)
	if err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: sig, Value: v, Unit: unit}
}

func intSignalValue(doc rawDoc, sig observation.SignalID, key, unit string) SignalValue {
	v, err := doc.intField(key)
	if err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: sig, Value: v, Unit: unit}
}

// elapsedMSSignal decodes fpp.position.elapsed.ms ("milliseconds_elapsed")
// on its own, not through modeGovernedTopLevelInt: F0's own capture found
// this key present only while status_name is a playing/stopping state and
// absent (not null, not zero) while idle within PLAYER mode, and absent on
// this package's own remote-mode fixtures too — a playback-state gate,
// not the player/remote MODE gate that helper models. An absent key is
// Unsupported with a reason naming the gate; CollectionFailed stays
// reserved for a present-but-undecodable field.
func elapsedMSSignal(doc rawDoc) SignalValue {
	v, err := doc.intField("milliseconds_elapsed")
	if err == nil {
		return SignalValue{Signal: SignalPositionElapsedMS, Value: v, Unit: "ms"}
	}
	if isFieldAbsent(err) {
		return SignalValue{
			Signal: SignalPositionElapsedMS, Absence: observation.StateUnsupported,
			Reason: `"milliseconds_elapsed" is present only while FPP is actively playing or stopping; absent here`,
		}
	}
	return SignalValue{Signal: SignalPositionElapsedMS, Absence: observation.StateCollectionFailed, Reason: err.Error()}
}

// --- mode-governed absence (contract section 3.3) --------------------------

// modeAbsenceReason returns the Unsupported reason for a signal whose
// source field is expected only when the host's FPP mode is expectedMode,
// when that is actually why the field is missing. ok is true only when
// ALL of the following hold:
//
//   - fieldErr is non-nil AND [isFieldAbsent] reports it as a genuine "key
//     not present" failure — never a present-but-undecodable field (wrong
//     JSON type, an explicit null, a non-integral number where an integer
//     was wanted, and so on). Review finding: an earlier version of this
//     function looked only at modeName/modeErr and ignored fieldErr
//     entirely, so injecting a present-but-malformed "repeat_mode" into a
//     real remote-mode capture reported absence="unsupported", reason
//     "host is in remote mode; FPP does not report a repeat mode" — FPP
//     DID report it; it just did not decode. Contract section 3.3
//     reserves collection_failed for exactly that case, and requires the
//     two to be distinguishable in the API.
//   - mode_name itself was read successfully (modeErr == nil) — otherwise
//     this package cannot even tell what mode the host is in, and
//     guessing would be worse than the plain collection_failed behavior.
//   - modeName is NOT expectedMode. If the host IS in expectedMode and the
//     field is still absent, that is a genuine anomaly this package does
//     not try to explain away as mode-governed.
func modeAbsenceReason(fieldErr error, modeName string, modeErr error, expectedMode, what string) (reason string, ok bool) {
	if !isFieldAbsent(fieldErr) {
		return "", false
	}
	if modeErr != nil || modeName == expectedMode {
		return "", false
	}
	return fmt.Sprintf("host is in %s mode; FPP does not report %s", modeName, what), true
}

func modeGovernedStringResult(sig observation.SignalID, v string, err error, modeName string, modeErr error, expectedMode, what string) SignalValue {
	if err == nil {
		return SignalValue{Signal: sig, Value: v}
	}
	if reason, ok := modeAbsenceReason(err, modeName, modeErr, expectedMode, what); ok {
		return SignalValue{Signal: sig, Absence: observation.StateUnsupported, Reason: reason}
	}
	return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
}

func modeGovernedBoolResult(sig observation.SignalID, v bool, err error, modeName string, modeErr error, expectedMode, what string) SignalValue {
	if err == nil {
		return SignalValue{Signal: sig, Value: v}
	}
	if reason, ok := modeAbsenceReason(err, modeName, modeErr, expectedMode, what); ok {
		return SignalValue{Signal: sig, Absence: observation.StateUnsupported, Reason: reason}
	}
	return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
}

func modeGovernedIntResult(sig observation.SignalID, v int64, err error, unit string, modeName string, modeErr error, expectedMode, what string) SignalValue {
	if err == nil {
		return SignalValue{Signal: sig, Value: v, Unit: unit}
	}
	if reason, ok := modeAbsenceReason(err, modeName, modeErr, expectedMode, what); ok {
		return SignalValue{Signal: sig, Absence: observation.StateUnsupported, Reason: reason}
	}
	return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
}

func modeGovernedTopLevelString(doc rawDoc, sig observation.SignalID, key string, modeName string, modeErr error, expectedMode, what string) SignalValue {
	v, err := doc.stringField(key)
	return modeGovernedStringResult(sig, v, err, modeName, modeErr, expectedMode, what)
}

func modeGovernedTopLevelInt(doc rawDoc, sig observation.SignalID, key, unit string, modeName string, modeErr error, expectedMode, what string) SignalValue {
	v, err := doc.intField(key)
	return modeGovernedIntResult(sig, v, err, unit, modeName, modeErr, expectedMode, what)
}

func modeGovernedNestedString(doc rawDoc, sig observation.SignalID, outerKey, innerKey string, modeName string, modeErr error, expectedMode, what string) SignalValue {
	v, err := doc.nestedStringField(outerKey, innerKey)
	return modeGovernedStringResult(sig, v, err, modeName, modeErr, expectedMode, what)
}

func modeGovernedNestedInt(doc rawDoc, sig observation.SignalID, outerKey, innerKey string, modeName string, modeErr error, expectedMode, what string) SignalValue {
	v, err := doc.nestedIntField(outerKey, innerKey)
	return modeGovernedIntResult(sig, v, err, "", modeName, modeErr, expectedMode, what)
}

// --- /api/fppd/ports ---------------------------------------------------

// PortSignals decodes an /api/fppd/ports response body (REST) or a
// port_status MQTT payload (same document, per contract section 4.3): a
// top-level JSON array, never an object. It returns an error only when
// body is not a JSON array at all.
//
// fpp.ports.count and fpp.ports.blind_count are always produced, including
// zero for both on fpp-player's captured empty array ([]) — an empty array
// is a measured fact about a Pi with no pixel output cape, not an absence
// (contract section 3.2).
//
// Decoding is deliberately two-pass, not one: a collision between element 0
// and element 5 is only detectable once element 5 has been seen, but on a
// single forward pass element 0's port signals would already have been
// emitted by then. Contract section 3.1 requires that a colliding key
// receive NO port signals at all — including the element that would
// otherwise "win" by appearing first (see the problems-reporting comment
// below) — so every element's key must be resolved before any element's
// signals are built. A single-pass version of this function was run and
// confirmed to leak the first-seen element's signals through despite the
// collision, contradicting that requirement; see
// TestPortsDecodeFailedNamesDuplicateKey.
func PortSignals(body []byte) ([]SignalValue, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(body, &elems); err != nil {
		return nil, fmt.Errorf("ports response body is not a JSON array: %w", err)
	}

	out := make([]SignalValue, 0, len(elems)*6+3)
	out = append(out, SignalValue{Signal: SignalPortsCount, Value: int64(len(elems))})

	var blind int64
	decoded := make([]rawDoc, len(elems))
	keyOf := make([]string, len(elems)) // "" means this element has no usable, unique key
	seenKeys := make(map[string]int, len(elems))
	duplicateKeys := make(map[string]bool)
	var problems []string

	// Pass 1: decode each element and resolve its key, without building any
	// signals yet.
	for i, raw := range elems {
		elem, err := decodeRawDoc(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("element %d: not a JSON object: %v", i, err))
			continue
		}
		decoded[i] = elem

		if _, hasMA := elem["ma"]; !hasMA {
			blind++
		}

		// name is the port's identity (contract section 3.1): never the
		// array index, which is not guaranteed stable across FPP
		// versions or configurations.
		name, nameErr := elem.stringField("name")
		key := ""
		if nameErr == nil {
			key = normalizeKey(name)
		}
		if nameErr != nil || key == "" {
			problems = append(problems, fmt.Sprintf("element %d: no usable \"name\" field", i))
			continue
		}
		if prev, dup := seenKeys[key]; dup {
			problems = append(problems, fmt.Sprintf(
				"element %d (name %q) normalizes to key %q, already used by element %d", i, name, key, prev))
			duplicateKeys[key] = true
			continue
		}
		seenKeys[key] = i
		keyOf[i] = key
	}

	// Pass 2: build port signals only for elements whose key is both
	// present and unique — see the doc comment above for why this cannot
	// be folded into pass 1.
	for i, elem := range decoded {
		key := keyOf[i]
		if key == "" || duplicateKeys[key] {
			continue
		}
		out = append(out, portElementSignals(elem, key)...)
	}

	out = append(out, SignalValue{Signal: SignalPortsBlindCount, Value: blind})

	if len(problems) > 0 {
		// One decode_failed observation naming every problem this poll
		// found, rather than one per bad element: contract section 3.1
		// asks for "no port signals for it" plus "one
		// fpp.ports.decode_failed collection_failed observation naming the
		// problem" per Poll call, and a single SignalID can only sensibly
		// carry one Observation per source per poll (see store schema v4 /
		// Seam C's precedence rule, which resolves per (resource, signal,
		// source) triple, not per occurrence).
		out = append(out, SignalValue{
			Signal:  SignalPortsDecodeFailed,
			Absence: observation.StateCollectionFailed,
			Reason:  strings.Join(problems, "; "),
		})
	}

	return out, nil
}

// portElementSignals builds every fpp.port.<key>.* signal for one port
// document element already known to have a usable, unique key. kind is
// "smart_receiver" when the element carries a "smartReceivers" key (per
// every live capture: 16-48 such positions per remote, none carrying "ma",
// "enabled", "status", or "bank" — see testdata/live_remote0*_fppd_ports.json)
// and "output" otherwise.
func portElementSignals(elem rawDoc, key string) []SignalValue {
	_, hasSR := elem["smartReceivers"]
	kind := "output"
	if hasSR {
		kind = "smart_receiver"
	}
	sig := func(suffix string) observation.SignalID {
		return observation.SignalID("fpp.port." + key + "." + suffix)
	}

	out := make([]SignalValue, 0, 6)
	out = append(out, SignalValue{Signal: sig("kind"), Value: kind})
	out = append(out, portCurrentMASignal(elem, sig("current_ma"), hasSR))
	out = append(out, portBoolField(elem, sig("enabled"), "enabled", hasSR))
	out = append(out, portBoolField(elem, sig("status"), "status", hasSR))
	out = append(out, portStringField(elem, sig("bank"), "bank", hasSR))
	out = append(out, portPixelCountField(elem, sig("pixel_count")))
	return out
}

// portCurrentMASignal implements the load-bearing absence rule contract
// section 3.2 exists for: a missing "ma" key is NEVER reported as a
// current reading, not even 0. On a smart-receiver position (hasSR) that
// is Unsupported with the exact reason contract section 3.2 specifies,
// verified against live_remote01_fppd_ports.json /
// live_remote04_fppd_ports.json, where every one of the 16/32
// smartReceivers-carrying elements has no "ma" key at all. On an output
// element it also falls back to Unsupported rather than fabricating a
// value: never observed missing "ma" on the live fleet (FPP 9.4/master),
// but expected on FPP 10.0 whenever a port has no current sensor —
// OutputMonitor::appendTo (src/OutputMonitor.cpp, confirmed against
// refs/tags/10.0^{}, commit 370e62ed7) only writes "ma" when
// currentMonitor is non-null, where 9.5.3 had no such gate.
func portCurrentMASignal(elem rawDoc, sig observation.SignalID, hasSR bool) SignalValue {
	raw, ok := elem["ma"]
	if !ok {
		if hasSR {
			return SignalValue{
				Signal:  sig,
				Absence: observation.StateUnsupported,
				Reason:  "smart receiver position: pre-V5 receivers report no per-port current",
			}
		}
		return SignalValue{Signal: sig, Absence: observation.StateUnsupported, Reason: "this port has no current sensor"}
	}
	// "ma" is PRESENT here (the ok=true branch): a JSON null value is a
	// different claim from the key being absent altogether (the branch
	// above) and must never fall through to json.Unmarshal, which decodes
	// null into a float64 as 0 with no error — exactly the "missing ma
	// reads as a measured 0 mA" fabrication contract section 3.2 exists to
	// forbid. See decode.go's isJSONNull doc comment.
	if isJSONNull(raw) {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: `field "ma" is explicitly null`}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: fmt.Sprintf(`field "ma": expected a number: %v`, err)}
	}
	return SignalValue{Signal: sig, Value: f, Unit: "milliamps"}
}

// portBoolField and portStringField implement contract section 3.2's
// second paragraph: enabled/status/bank are Unsupported (not omitted) on a
// smart-receiver position, each with a reason naming which key was
// absent, and are ordinary values on an output element on the live
// fleet (FPP 9.4/master, where all three are always present). On FPP
// 10.0, enabled and status are each Unsupported instead whenever the
// port has no enable pin or no eFuse respectively — see
// portAbsentReason's doc comment.
func portBoolField(elem rawDoc, sig observation.SignalID, key string, hasSR bool) SignalValue {
	raw, ok := elem[key]
	if !ok {
		return SignalValue{Signal: sig, Absence: observation.StateUnsupported, Reason: portAbsentReason(key, hasSR)}
	}
	// See portCurrentMASignal's identical comment: present-but-null must
	// never fall through to json.Unmarshal, which would decode null as
	// false with no error.
	if isJSONNull(raw) {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: fmt.Sprintf("field %q is explicitly null", key)}
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: fmt.Sprintf("field %q: expected a bool: %v", key, err)}
	}
	return SignalValue{Signal: sig, Value: b}
}

func portStringField(elem rawDoc, sig observation.SignalID, key string, hasSR bool) SignalValue {
	raw, ok := elem[key]
	if !ok {
		return SignalValue{Signal: sig, Absence: observation.StateUnsupported, Reason: portAbsentReason(key, hasSR)}
	}
	// See portCurrentMASignal's identical comment: present-but-null must
	// never fall through to json.Unmarshal, which would decode null as ""
	// with no error.
	if isJSONNull(raw) {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: fmt.Sprintf("field %q is explicitly null", key)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: fmt.Sprintf("field %q: expected a string: %v", key, err)}
	}
	return SignalValue{Signal: sig, Value: s}
}

// portAbsentReason names why key is missing from a non-smart-receiver port
// element. On FPP 10.0, OutputMonitor::appendTo (src/OutputMonitor.cpp,
// confirmed against refs/tags/10.0^{}, commit 370e62ed7) only sets
// "enabled" when the port has an enable pin, only sets "status" when it
// has an eFuse pin, and (per portCurrentMASignal) only sets "ma" when it
// has a current sensor — each key is conditionally written, not always
// present with a placeholder value. FPP 9.5.3 always wrote a hardcoded
// "status": true regardless of hardware, so this three-way omission is new
// in 10.0. It is normal hardware reporting, not a decode anomaly.
func portAbsentReason(key string, hasSR bool) string {
	if hasSR {
		return fmt.Sprintf("smart receiver position: no %q key reported", key)
	}
	switch key {
	case "enabled":
		return "this port has no enable pin"
	case "status":
		return "this port has no eFuse"
	default:
		return fmt.Sprintf("%q absent from this port element", key)
	}
}

// portPixelCountField implements contract section 3.2's third absence
// rule: pixelCount is absent from EVERY element in every live capture,
// output or smart-receiver alike, and its absence must read as
// Unsupported with a reason that says plainly the pixel-count operation
// has never been run — never a silently reported 0.
func portPixelCountField(elem rawDoc, sig observation.SignalID) SignalValue {
	v, err := elem.intField("pixelCount")
	if err != nil {
		if _, ok := elem["pixelCount"]; !ok {
			return SignalValue{
				Signal:  sig,
				Absence: observation.StateUnsupported,
				Reason:  "pixelCount absent from this FPP's port document; the pixel-count operation has never been run on this host",
			}
		}
		return SignalValue{Signal: sig, Absence: observation.StateCollectionFailed, Reason: err.Error()}
	}
	return SignalValue{Signal: sig, Value: v}
}

// --- /api/system/info ----------------------------------------------------

// SystemInfoSignals decodes an /api/system/info response body into the
// platform signals of contract section 3.1's table. It returns an error
// only when body is not a JSON object at all; a missing or malformed
// "Utilization" object (or its nested "Disk" object) degrades only the
// utilization/disk signals, never the platform-identity ones decoded
// before it.
func SystemInfoSignals(body []byte) ([]SignalValue, error) {
	doc, err := decodeRawDoc(body)
	if err != nil {
		return nil, err
	}

	out := make([]SignalValue, 0, len(systemInfoStaticSignals))
	out = append(out, stringSignalValue(doc, SignalOSVersion, "OSVersion"))
	out = append(out, stringSignalValue(doc, SignalOSRelease, "OSRelease"))
	out = append(out, stringSignalValue(doc, SignalPlatform, "Platform"))
	out = append(out, stringSignalValue(doc, SignalVariant, "Variant"))
	out = append(out, stringSignalValue(doc, SignalKernel, "Kernel"))
	out = append(out, intSignalValue(doc, SignalMajorVersion, "majorVersion", ""))
	out = append(out, intSignalValue(doc, SignalMinorVersion, "minorVersion", ""))

	util, err := doc.objectField("Utilization")
	if err != nil {
		out = append(out, utilizationFailure(err.Error())...)
		return out, nil
	}
	out = append(out, numberSignalValue(util, SignalUtilizationCPU, "CPU", "percent"))
	out = append(out, numberSignalValue(util, SignalUtilizationMem, "Memory", "percent"))

	disk, err := util.objectField("Disk")
	if err != nil {
		out = append(out, diskFailure(err.Error())...)
		return out, nil
	}
	out = append(out, diskFreeTotalSignals(disk, "Media", SignalDiskMediaFree, SignalDiskMediaTotal)...)
	out = append(out, diskFreeTotalSignals(disk, "Root", SignalDiskRootFree, SignalDiskRootTotal)...)

	return out, nil
}

func utilizationFailure(reason string) []SignalValue {
	sigs := []observation.SignalID{SignalUtilizationCPU, SignalUtilizationMem}
	return append(sigs2SignalValues(sigs, reason), diskFailure(reason)...)
}

func diskFailure(reason string) []SignalValue {
	sigs := []observation.SignalID{SignalDiskMediaFree, SignalDiskMediaTotal, SignalDiskRootFree, SignalDiskRootTotal}
	return sigs2SignalValues(sigs, reason)
}

func sigs2SignalValues(sigs []observation.SignalID, reason string) []SignalValue {
	out := make([]SignalValue, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, SignalValue{Signal: s, Absence: observation.StateCollectionFailed, Reason: reason})
	}
	return out
}

func diskFreeTotalSignals(disk rawDoc, key string, freeSig, totalSig observation.SignalID) []SignalValue {
	section, err := disk.objectField(key)
	if err != nil {
		return sigs2SignalValues([]observation.SignalID{freeSig, totalSig}, err.Error())
	}
	return []SignalValue{
		intSignalValue(section, freeSig, "Free", "bytes"),
		intSignalValue(section, totalSig, "Total", "bytes"),
	}
}
