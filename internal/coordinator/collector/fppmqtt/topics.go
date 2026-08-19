package fppmqtt

import (
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// topicSpec pairs one topic suffix's decoder with the signal IDs a
// caller must fall back to (as StateCollectionFailed or StateNotCollected)
// when that topic's payload cannot be decoded at all, or has never
// delivered a message yet. staticSignals lists only signals that can be
// enumerated ahead of ever seeing a real payload — per-port
// (fpp.port.<key>.*) and per-sensor (fpp.sensor.<key>.*) signals cannot be,
// the same reasoning apiwiring.go's fppSignals restricts itself to for the
// REST collector's own not-yet-polled placeholders.
type topicSpec struct {
	decode        func(body []byte) ([]fpp.SignalValue, error)
	staticSignals []observation.SignalID
}

// statusTopicExcludedSignals is exactly the seven signals fpp.StatusSignals
// decodes from /api/fppd/status's document that this package does NOT
// re-emit from the fppd_status topic, because each already has its own
// dedicated MQTT topic below (version, branch, status,
// playlist/repeat/status, playlist/position/status) or its own dedicated
// "warnings" topic (fpp.warnings.count, fpp.warnings.summary). See this
// var block's motivating comment on topicSpecs, a few lines down, for why
// that exclusion has to exist at all.
var statusTopicExcludedSignals = map[observation.SignalID]bool{
	SignalVersion:            true,
	SignalBranch:             true,
	SignalStatus:             true,
	SignalPlaylistRepeatMode: true,
	SignalPlaylistIndex:      true,
	SignalWarningsCount:      true,
	SignalWarningsSummary:    true,
}

// decodeStatusTopic decodes the fppd_status MQTT payload using
// fpp.StatusSignals — the SAME decoder
// internal/coordinator/collector/fpp's REST collector uses for the
// identical /api/fppd/status document (contract section 4.3: "the decoding
// logic is the same. Do not duplicate it.") — then removes exactly the
// signals in statusTopicExcludedSignals before returning. This is the
// fold-in this package's decode.go used to carry a STUB NOTICE about: one
// decoder, not two, with the topic-ownership exclusion applied as an
// explicit filter at this seam rather than baked into a second copy of the
// decode logic. decode_test.go's cross-package agreement tests prove this
// filter is the ONLY difference between what fpp.StatusSignals produces and
// what this topic emits.
func decodeStatusTopic(body []byte) ([]fpp.SignalValue, error) {
	all, err := fpp.StatusSignals(body)
	if err != nil {
		return nil, err
	}
	out := make([]fpp.SignalValue, 0, len(all))
	for _, sv := range all {
		if statusTopicExcludedSignals[sv.Signal] {
			continue
		}
		out = append(out, sv)
	}
	return out, nil
}

// statusStaticSignals is exactly the set of fixed (non-sensor) signal IDs
// decodeStatusTopic can produce (fpp.StatusSignals' own output, minus
// statusTopicExcludedSignals). Kept in sync with that function by
// decode_test.go's TestStatusSignalsStayWithinStatusStaticSignals, rather
// than derived automatically, because StatusSignals' mode-dependent
// branches make automatic derivation from a single sample document
// unreliable (a player-mode capture will never exercise the remote-only
// branch, and vice versa).
var statusStaticSignals = []observation.SignalID{
	SignalMode,
	SignalPlaylistName,
	SignalSequenceName,
	SignalPositionSeconds,
	SignalPositionRemaining,
	SignalMultiSyncEnabled,
	SignalUptimeSeconds,
	SignalSongName,
	SignalFppdState,
	SignalPowerBad,
	SignalBridging,
	SignalChannelInputsEnabled,
	SignalChannelOutputsEnabled,
	SignalUUID,
	SignalHostName,
	SignalVolume,
	SignalMQTTConfigured,
	SignalMQTTConnected,
	SignalSchedulerStatus,
	SignalPlaylistCount,
	SignalPlaylistType,
	SignalSchedulerEnabled,
	SignalSchedulerNextPlaylist,
	SignalSchedulerNextStartTime,
	SignalMediaFilename,
	SignalPositionElapsedSeconds,
	SignalPositionElapsedMS,
}

// portStaticSignals is the enumerable subset of fpp.PortSignals' output:
// fpp.ports.count and fpp.ports.blind_count always exist once a
// port_status payload decodes; fpp.port.<key>.* cannot be enumerated ahead
// of a real payload (the key comes from the device's own reported name),
// and fpp.ports.decode_failed only appears when there is a problem to
// report, so neither belongs in a "not yet collected" placeholder list.
var portStaticSignals = []observation.SignalID{
	SignalPortsCount,
	SignalPortsBlindCount,
}

// topicSpecs is the contract section 4.3 topic table: every MQTT topic
// suffix this collector models, under "<prefix>/<HostName>/", and how to
// turn its payload into signals.
//
// This is an EXPLICIT list, subscribed to one filter per entry per host
// (see mqttclient.go's subscribeAll) — deliberately NOT a single
// "<prefix>/<host>/#" wildcard subscription, even though contract section
// 4.3's prose describes that wildcard. The very next sentence in that same
// section says "Do not subscribe to falcon/control/# or to any command/*
// topic" (section 0's live command topics,
// "falcon/player/<host>/command/run", live directly under this same host
// subtree per section 0's own naming). A host-scoped "#" would technically
// still receive that topic; an explicit per-suffix subscription list
// cannot, structurally, because "command/run" is never one of the entries
// below. That is a strictly stronger, machine-checkable form of the same
// safety property section 4.5 asks for elsewhere ("not 'does not call
// publish' but 'cannot'"), applied to subscriptions instead of publishes —
// see readonly_test.go's TestSubscriptionFiltersNeverIncludeCommandTopics,
// which enumerates every filter this package would ever ask the broker for
// and asserts none of them can match a command topic.
//
// Two signals in contract section 3.1's table (fpp.warnings.count and
// fpp.warnings.summary) and several more (fpp.version, fpp.branch,
// fpp.status, fpp.playlist.repeat_mode, fpp.playlist.index) are listed
// there as status-document-derived, but this package derives them ONLY
// from their own dedicated topics below, never additionally from
// fppd_status's overlapping fields. A single Poll call producing two
// observations for the same (resource, signal, source) is not a
// documented case anywhere in pkg/observation or the store's schema — the
// schema v4 migration (contract section 5.1) only disambiguates across
// DIFFERENT sources, not two deliveries from the SAME source in one poll —
// so this package treats "one topic owns one signal" as a hard invariant
// of its own topicSpecs table rather than letting it become untested,
// undefined behavior. decode_test.go's
// TestNoSignalIDAppearsInMoreThanOneTopicSpec enforces this statically, and
// TestPollNeverDuplicatesAnExcludedSignalAcrossTopics enforces the dynamic
// half of the same invariant — that no combination of delivered messages
// makes Poll itself emit one of these signals twice — against Poll's
// actual output rather than against this table.
//
// The "fppd_status" entry below is exactly how this exclusion is enforced
// in practice: it decodes via fpp.StatusSignals (the same function
// internal/coordinator/collector/fpp's REST collector calls for the
// identical document, per contract section 4.3), then
// decodeStatusTopic strips statusTopicExcludedSignals — the seven signals
// named in this comment — from that decoder's output before this topic's
// signals ever reach Poll. See decodeStatusTopic's own doc comment, a few
// lines above this var block.
var topicSpecs = map[string]topicSpec{
	"fppd_status":              {decode: decodeStatusTopic, staticSignals: statusStaticSignals},
	"port_status":              {decode: fpp.PortSignals, staticSignals: portStaticSignals},
	"warnings":                 {decode: warningsSignals, staticSignals: []observation.SignalID{SignalWarningsCount, SignalWarningsSummary}},
	"version":                  {decode: rawTextStringSignal(SignalVersion), staticSignals: []observation.SignalID{SignalVersion}},
	"branch":                   {decode: rawTextStringSignal(SignalBranch), staticSignals: []observation.SignalID{SignalBranch}},
	"status":                   {decode: rawTextStringSignal(SignalStatus), staticSignals: []observation.SignalID{SignalStatus}},
	"ready":                    {decode: readySignal, staticSignals: []observation.SignalID{SignalReady}},
	"playlist/repeat/status":   {decode: rawTextIntSignal(SignalPlaylistRepeatMode), staticSignals: []observation.SignalID{SignalPlaylistRepeatMode}},
	"playlist/position/status": {decode: rawTextIntSignal(SignalPlaylistIndex), staticSignals: []observation.SignalID{SignalPlaylistIndex}},
}

// allStaticSignalIDs is the union of every topicSpec's staticSignals,
// de-duplicated and sorted for deterministic iteration. It is what
// render.go's connection-down handling uses for contract section 4.1's
// "a collection_failed on every signal ... when the connection is down" —
// per-port/per-sensor signals are excluded for the same "cannot enumerate
// ahead of a real payload" reason staticSignals excludes them everywhere
// else.
var allStaticSignalIDs = computeAllStaticSignalIDs()

func computeAllStaticSignalIDs() []observation.SignalID {
	seen := make(map[observation.SignalID]bool)
	for _, spec := range topicSpecs {
		for _, sig := range spec.staticSignals {
			seen[sig] = true
		}
	}
	out := make([]observation.SignalID, 0, len(seen))
	for sig := range seen {
		out = append(out, sig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
