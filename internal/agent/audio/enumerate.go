package audio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// commandRunner runs name with args and returns its combined stdout+stderr,
// substituted in tests so aplay parsing can be proven without a real ALSA
// stack on the test host.
type commandRunner func(ctx context.Context, name string, args ...string) (string, error)

var runCommand commandRunner = runRealCommand

func runRealCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// virtualDeviceNames are PCM device names ALSA reports even with no real
// interface attached — MEASURED, bench/audio-node/results/
// r7_capability_discovery.json, a container with no /dev/snd: never a
// candidate route for a real output.
var virtualDeviceNames = map[string]struct{}{
	"null":    {},
	"default": {},
}

// Enumerator reports what this node's ALSA stack currently exposes. A real
// implementation ([AlsaEnumerator]) shells `aplay`; tests substitute a fake.
type Enumerator interface {
	// Devices returns every PCM device name `aplay -L` reports, including
	// "null" and "default" — [CandidateDevices] is what filters those out.
	Devices(ctx context.Context) ([]string, error)

	// HasHardwareCards reports whether `aplay -l` finds at least one real
	// sound card. False, with no error, is the expected answer on a
	// genuinely card-less host — MEASURED, r7_capability_discovery.log:
	// "aplay -l" exits reporting "no soundcards found" while "aplay -L"
	// still lists the virtual null/default devices.
	HasHardwareCards(ctx context.Context) (bool, error)
}

// AlsaEnumerator is the real [Enumerator]: shells `aplay -L` and `aplay -l`.
type AlsaEnumerator struct{}

// Devices implements [Enumerator].
func (AlsaEnumerator) Devices(ctx context.Context) ([]string, error) {
	out, err := runCommand(ctx, "aplay", "-L")
	if err != nil {
		return nil, fmt.Errorf("audio: aplay -L: %w", err)
	}
	return parseAplayL(out), nil
}

// HasHardwareCards implements [Enumerator]. A permission failure, a
// missing aplay binary, a timeout, or any other transport error is
// reported as an error rather than folded into "no
// hardware": [Discovery.HardwareEnumerated] already exists precisely to
// carry "we do not know yet" separately from "confirmed absent", but
// only if this method actually returns the error instead of discarding
// it, as an earlier version did.
func (AlsaEnumerator) HasHardwareCards(ctx context.Context) (bool, error) {
	// aplay -l exits non-zero on a card-less host (MEASURED above); that
	// exit is the expected "no hardware" answer, never a transport error
	// — distinguished from a genuine failure by the marker text actually
	// being present, not by the exit code alone.
	out, err := runCommand(ctx, "aplay", "-l")
	if err != nil && !strings.Contains(out, aplayNoCardsMarker) {
		return false, fmt.Errorf("audio: aplay -l: %w", err)
	}
	return parseAplayLHasCards(out), nil
}

// parseAplayL extracts PCM device names from `aplay -L` output: a line with
// no leading whitespace is a device name; an indented line under it is that
// device's human-readable description and is discarded.
func parseAplayL(out string) []string {
	var devices []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		devices = append(devices, strings.TrimSpace(line))
	}
	return devices
}

// aplayNoCardsMarker is the exact substring MEASURED (r7_capability_
// discovery.log) on a genuinely card-less host.
const aplayNoCardsMarker = "no soundcards found"

// parseAplayLHasCards reports whether `aplay -l` output names at least one
// real card.
func parseAplayLHasCards(out string) bool {
	if strings.Contains(out, aplayNoCardsMarker) {
		return false
	}
	return strings.Contains(out, "card ")
}

// alsaDirectPrefixes are the ALSA PCM name prefixes this package treats as
// the device itself: "hw" is the raw hardware device, "plughw" adds only
// format conversion. Every other prefix ("sysdefault", "front",
// "surround51", "hdmi", "dmix", ...) is an ALSA-level alias or a
// mixed/software route that can silently coexist with, and shadow, the
// real device sharing its card — [CandidateDevices] prefers a direct
// prefix over any of those for the same card.
var alsaDirectPrefixes = map[string]bool{"hw": true, "plughw": true}

// alsaCardKey extracts the card identity portion of an ALSA PCM device
// name, e.g. "PCH" from both "hw:CARD=PCH,DEV=0" and
// "sysdefault:CARD=PCH", or the full suffix after the colon when no
// "CARD=" token is present (e.g. "0,0" from "hw:0,0") — this is what lets
// [CandidateDevices] tell two aliases of the same card apart from two
// different cards.
func alsaCardKey(device string) string {
	_, suffix, ok := strings.Cut(device, ":")
	if !ok {
		return device
	}
	for _, part := range strings.Split(suffix, ",") {
		if v, ok := strings.CutPrefix(part, "CARD="); ok {
			return v
		}
	}
	return suffix
}

// PipeWireNode is one Audio/Sink node a PipeWire graph reports, with the
// real channel count and sample rate PipeWire itself negotiated for it —
// a graph fact, not an ALSA probe result.
type PipeWireNode struct {
	Name        string
	Description string
	Channels    int
	Rate        int
}

// PipeWireEnumerator reports what this node's PipeWire graph currently
// exposes. A real implementation ([PwDumpEnumerator]) shells `pw-dump`.
type PipeWireEnumerator interface {
	// Nodes returns every Audio/Sink node pw-dump's output describes.
	// present is false with a nil error when pw-dump itself failed to
	// run (no PipeWire on this host at all) — a clean absence, never an
	// enumeration failure. err is non-nil only when pw-dump ran but its
	// output could not be parsed, which IS a failure a caller must
	// report rather than read as "no PipeWire".
	Nodes(ctx context.Context) (nodes []PipeWireNode, present bool, err error)
}

// PwDumpEnumerator is the real [PipeWireEnumerator]: shells `pw-dump`.
type PwDumpEnumerator struct{}

// Nodes implements [PipeWireEnumerator]. Only pw-dump itself not being on
// this host (exec.ErrNotFound) is a clean absence: a node with no
// PipeWire package installed at all. pw-dump found but exiting non-zero
// -- MEASURED on node-01, EACCES connecting to a socket this user lacks
// the write bit on -- is present=true with the failure text, never
// folded into "no PipeWire": a sinkBackend=pipewiresink node reading
// this as absence reported "no device probe is possible while PipeWire
// holds the card" for a graph it was never able to reach in the first
// place.
func (PwDumpEnumerator) Nodes(ctx context.Context) ([]PipeWireNode, bool, error) {
	out, err := runCommand(ctx, "pw-dump")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("audio: pw-dump: %w (output: %s)", err, strings.TrimSpace(out))
	}
	nodes, err := parsePwDumpSinkNodes(out)
	if err != nil {
		return nil, true, fmt.Errorf("audio: pw-dump: %w", err)
	}
	return nodes, true, nil
}

// pwDumpObject is the subset of one `pw-dump` array entry this package
// reads: the object's type and its info.props map, which carries every
// property key pw-dump reports (node.name, node.description,
// media.class, audio.channels, audio.rate, ...) under loosely-typed JSON
// values.
type pwDumpObject struct {
	Type string `json:"type"`
	Info struct {
		Props map[string]any `json:"props"`
	} `json:"info"`
}

// pwDumpNodeType and pwDumpSinkMediaClass are the exact object type and
// media.class `pw-dump` uses for an audio output node.
const (
	pwDumpNodeType       = "PipeWire:Interface:Node"
	pwDumpSinkMediaClass = "Audio/Sink"
)

// parsePwDumpSinkNodes decodes `pw-dump`'s JSON array and returns every
// Audio/Sink node it describes. A node missing node.name is skipped: it
// cannot be named as a route. audio.channels/audio.rate absent or
// unparseable leave Channels/Rate at 0 rather than failing the whole
// parse, since a node PipeWire has not finished negotiating is real
// evidence too, just not yet a usable one.
func parsePwDumpSinkNodes(out string) ([]PipeWireNode, error) {
	var objects []pwDumpObject
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &objects); err != nil {
		return nil, fmt.Errorf("decoding pw-dump JSON: %w", err)
	}
	var nodes []PipeWireNode
	for _, obj := range objects {
		if obj.Type != pwDumpNodeType {
			continue
		}
		if mc, _ := obj.Info.Props["media.class"].(string); mc != pwDumpSinkMediaClass {
			continue
		}
		name, _ := obj.Info.Props["node.name"].(string)
		if name == "" {
			continue
		}
		desc, _ := obj.Info.Props["node.description"].(string)
		nodes = append(nodes, PipeWireNode{
			Name:        name,
			Description: desc,
			Channels:    pwDumpPropInt(obj.Info.Props, "audio.channels"),
			Rate:        pwDumpPropInt(obj.Info.Props, "audio.rate"),
		})
	}
	return nodes, nil
}

// pwDumpPropInt reads a numeric pw-dump property. encoding/json decodes
// every JSON number into float64 when the target is `any`, so this
// truncates that back to int; a missing or non-numeric key reports 0.
func pwDumpPropInt(props map[string]any, key string) int {
	v, ok := props[key].(float64)
	if !ok {
		return 0
	}
	return int(v)
}

// CandidateDevices filters devices down to routes worth probing for a real
// output: virtual-only names removed, the whole list emptied whenever
// hasHardwareCards is false (a node with no audio hardware advertises no
// audio output capability, and "null"/"default" alone must never be
// presented as evidence of an interface), and — because ALSA names many
// aliases for one physical card ("hw:", "plughw:", "sysdefault:", "front:",
// "surround51:", "hdmi:", "dmix:", ...) — deduplicated to at most one
// candidate per card, preferring a direct hw:/plughw: name so a card with
// many aliases cannot fill every probe slot before a second real card is
// ever reached.
func CandidateDevices(devices []string, hasHardwareCards bool) []string {
	if !hasHardwareCards {
		return nil
	}
	bestByCard := make(map[string]string)
	var cardOrder []string
	for _, d := range devices {
		if _, virtual := virtualDeviceNames[d]; virtual {
			continue
		}
		prefix, _, _ := strings.Cut(d, ":")
		key := alsaCardKey(d)

		existing, seen := bestByCard[key]
		if !seen {
			bestByCard[key] = d
			cardOrder = append(cardOrder, key)
			continue
		}
		existingPrefix, _, _ := strings.Cut(existing, ":")
		if alsaDirectPrefixes[prefix] && !alsaDirectPrefixes[existingPrefix] {
			bestByCard[key] = d
		}
	}
	if len(cardOrder) == 0 {
		return nil
	}
	out := make([]string, 0, len(cardOrder))
	for _, key := range cardOrder {
		out = append(out, bestByCard[key])
	}
	return out
}
