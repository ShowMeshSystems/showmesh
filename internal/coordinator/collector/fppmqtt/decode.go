// This file decodes the MQTT topics that have no REST equivalent: the
// "warnings" topic (structurally different from /api/fppd/status's own
// "warnings" field — see warningsSignals below) and the single-value
// raw-text topics (version, branch, status, ready, playlist/repeat/status,
// playlist/position/status).
//
// The fppd_status and port_status topics are NOT decoded here: they carry
// the same documents /api/fppd/status and /api/fppd/ports return (contract
// section 4.3), so they are decoded by
// internal/coordinator/collector/fpp.StatusSignals and .PortSignals — the
// single producer of that decode logic — via topics.go's decodeStatusTopic
// and the direct fpp.PortSignals reference in topicSpecs. This file
// previously carried its own independent copy of that decoding (marked
// STUB NOTICE, TO BE DELETED AT FOLD-IN, written before the fpp package
// exported those decoders); that copy is gone, and topics.go's doc comment
// explains the one thing that still differs between the two paths — the
// per-topic signal exclusion applied to fpp.StatusSignals' output.
package fppmqtt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// measuredValue is the only SignalValue constructor this file still needs:
// every topic decoded here (warnings, version, branch, status, ready,
// playlist/repeat/status, playlist/position/status) either succeeds with a
// value or returns a decode error — none of them has a per-field absence
// case the way fpp.StatusSignals/fpp.PortSignals do, so this file carries
// no unsupportedValue/failedValue constructors of its own (fold-in deleted
// the last callers those had).
func measuredValue(sig observation.SignalID, value any, unit string) fpp.SignalValue {
	return fpp.SignalValue{Signal: sig, Value: value, Unit: unit}
}

// --- warningsSignals: the dedicated "warnings" topic ----------------------

// warningsSignals decodes the "warnings" MQTT topic. Verified against
// testdata/FPP-Main_warnings.json and testdata/FPP-remote-04_warnings.json:
// the payload is a JSON array of {"id":<int>,"message":<string>} objects —
// structurally the same shape as /api/fppd/status's "warningInfo" field,
// NOT its plain-string "warnings" field, despite the MQTT topic being named
// "warnings". This decoder tolerates a plain array of strings too, in case
// a differently-configured FPP build ever sends that shape instead — the
// count and the joined summary mean the same thing either way.
func warningsSignals(body []byte) ([]fpp.SignalValue, error) {
	var objects []struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &objects); err == nil {
		return warningsFromMessages(messagesFrom(objects)), nil
	}

	var strs []string
	if err := json.Unmarshal(body, &strs); err == nil {
		return warningsFromMessages(strs), nil
	}

	return nil, fmt.Errorf("response body is not a JSON array of strings or {message} objects")
}

func messagesFrom(objects []struct {
	Message string `json:"message"`
}) []string {
	out := make([]string, len(objects))
	for i, o := range objects {
		out[i] = o.Message
	}
	return out
}

func warningsFromMessages(messages []string) []fpp.SignalValue {
	return []fpp.SignalValue{
		measuredValue(SignalWarningsCount, int64(len(messages)), ""),
		measuredValue(SignalWarningsSummary, strings.Join(messages, "; "), ""),
	}
}

// --- single-value topic decoders -------------------------------------------
//
// version, branch, status, ready, playlist/repeat/status, and
// playlist/position/status all publish their value as PLAIN TEXT, not a
// JSON-quoted string — verified against testdata/FPP-Main_status.json
// (payload bytes are exactly `idle`, four bytes, no quotes),
// testdata/FPP-Main_ready.json (`1`), and
// testdata/FPP-Main_playlist_repeat_status.json (`0`). json.Unmarshal would
// reject `idle` outright (not valid JSON), so these topics are read as raw
// text, trimmed of surrounding whitespace, never JSON-decoded.

func rawTextStringSignal(sig observation.SignalID) func([]byte) ([]fpp.SignalValue, error) {
	return func(body []byte) ([]fpp.SignalValue, error) {
		return []fpp.SignalValue{measuredValue(sig, strings.TrimSpace(string(body)), "")}, nil
	}
}

func readySignal(body []byte) ([]fpp.SignalValue, error) {
	text := strings.TrimSpace(string(body))
	switch text {
	case "1":
		return []fpp.SignalValue{measuredValue(SignalReady, true, "")}, nil
	case "0":
		return []fpp.SignalValue{measuredValue(SignalReady, false, "")}, nil
	default:
		return nil, fmt.Errorf("unexpected ready value %q, want \"0\" or \"1\"", text)
	}
}

func rawTextIntSignal(sig observation.SignalID) func([]byte) ([]fpp.SignalValue, error) {
	return func(body []byte) ([]fpp.SignalValue, error) {
		text := strings.TrimSpace(string(body))
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected value %q, want an integer: %w", text, err)
		}
		return []fpp.SignalValue{measuredValue(sig, n, "")}, nil
	}
}
