package audio

import (
	"bufio"
	"context"
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

// HasHardwareCards implements [Enumerator].
func (AlsaEnumerator) HasHardwareCards(ctx context.Context) (bool, error) {
	// aplay -l exits non-zero on a card-less host (MEASURED above); that
	// exit is the expected "no hardware" answer, never a transport error.
	out, _ := runCommand(ctx, "aplay", "-l")
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
