package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file is this node's held FPP Connect state: what the coordinator's
// "fppconnect.configure" push (internal/coordinator/fppconnectpush) most
// recently told it, channel ranges, active show, show name list, and the
// byte-cap settings (ADR-044 decision 5). fppconnectops.go's operation
// writes it; the UDP responder (multisync.go) and HTTP listener a later
// seam builds read it. Persisted under AssetDir (see [fppConnectState.
// Save]/[fppConnectState.Load]) so a restart answers from disk rather than
// from nothing, matching internal/agent/heldcatalog's identical durability
// contract for the held Cue catalog.

// ErrChannelRangesTooLong is returned by [fppConnectState.SetChannelRanges]
// when v exceeds multisync.MaxPingRangesLength bytes, the most the ping
// packet's Ranges field can hold. Without this check a longer value would
// silently fail to encode later (see discoverResponse in multisync.go),
// which answers no discover ping at all rather than reporting the error at
// the point it was introduced.
var ErrChannelRangesTooLong = errors.New("fppconnect: channel ranges string exceeds the ping Ranges field capacity")

// fppConnectState holds this node's currently-applied FPP Connect
// configuration, shared between the MultiSync discover-ping responder and
// this seam's coordinator push. The Channel Ranges field and its two
// accessors are the minimal shape a sibling seam building the UDP ping
// responder depends on directly.
type fppConnectState struct {
	mu sync.RWMutex

	channelRanges string

	// activeShowKnown/activeShowName are the tri-state ADR-044 decision 5
	// requires: never touched by SetActiveShow (activeShowKnown false,
	// activeShowEverSet false) means this node has never been told an
	// active show at all; activeShowEverSet true with activeShowKnown
	// false means the coordinator explicitly pushed "no active show"
	// (JSON null); activeShowKnown true means activeShowName is the
	// pushed show's display name. Absent, null, and empty stay three
	// different things (ADR-039 decision 5) all the way down to this
	// node's own memory of them.
	activeShowEverSet bool
	activeShowKnown   bool
	activeShowName    string

	showNames []string

	settingsEverSet bool
	settings        fppConnectSettings
}

// fppConnectSettings mirrors internal/coordinator/config.
// FPPConnectSettingsPayload's JSON tags exactly, independently
// reproduced, not imported: this package has no coordinator dependency,
// matching every other wire boundary in this codebase (see
// audionodeops.go's identical audioSettingsConfig).
type fppConnectSettings struct {
	Enabled          bool  `json:"enabled"`
	MaxFileBytes     int64 `json:"maxFileBytes"`
	MaxAssetDirBytes int64 `json:"maxAssetDirBytes"`
}

// fppConnectSnapshot is [fppConnectState]'s whole applied state as one
// value: what one "fppconnect.configure" push replaces atomically (see
// [fppConnectState.Apply]), and the exact shape [fppConnectState.Save]
// persists and [fppConnectState.Load] restores.
type fppConnectSnapshot struct {
	ChannelRanges     string             `json:"channelRanges"`
	ActiveShowEverSet bool               `json:"activeShowEverSet"`
	ActiveShowKnown   bool               `json:"activeShowKnown"`
	ActiveShowName    string             `json:"activeShowName"`
	ShowNames         []string           `json:"showNames"`
	SettingsEverSet   bool               `json:"settingsEverSet"`
	Settings          fppConnectSettings `json:"settings"`
}

// newFPPConnectState returns an empty holder: no channel ranges, no
// active show ever set, no show names, no settings ever set. Matches
// every other holder in this package (e.g. newMultiSyncStatus,
// newAudioBinding) in taking no arguments and being safe to use
// immediately.
func newFPPConnectState() *fppConnectState {
	return &fppConnectState{}
}

// ChannelRanges returns the currently held channel ranges string, "" both
// before anything has ever been pushed and after a push that legitimately
// carries no ranges (a node with no configured show.surface).
func (s *fppConnectState) ChannelRanges() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channelRanges
}

// SetChannelRanges replaces the held channel ranges string. It refuses,
// and keeps the previous value, when v exceeds multisync.
// MaxPingRangesLength bytes: without this check a longer value would
// silently fail to encode later (see discoverResponse in multisync.go),
// which answers no discover ping at all rather than reporting the error at
// the point it was introduced.
func (s *fppConnectState) SetChannelRanges(v string) error {
	if len(v) > multisync.MaxPingRangesLength {
		return fmt.Errorf("%w: %d bytes, limit is %d", ErrChannelRangesTooLong, len(v), multisync.MaxPingRangesLength)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelRanges = v
	return nil
}

// ActiveShow reports the currently held active show. ever is false only
// before this node has ever been told an active show at all; in that
// case known and name are meaningless. Once ever is true, known
// distinguishes an explicit "no active show" (known false) from a named
// active show (known true, name is its display name).
func (s *fppConnectState) ActiveShow() (name string, known bool, ever bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeShowName, s.activeShowKnown, s.activeShowEverSet
}

// SetActiveShow records the coordinator's pushed active show: nil means
// "no active show" (an explicit push, not "unknown"), a non-nil pointer
// carries the active show's display name.
func (s *fppConnectState) SetActiveShow(name *string) {
	s.mu.Lock()
	s.activeShowEverSet = true
	if name == nil {
		s.activeShowKnown = false
		s.activeShowName = ""
	} else {
		s.activeShowKnown = true
		s.activeShowName = *name
	}
	s.mu.Unlock()
}

// ShowNames returns a copy of the currently held show name list, nil
// before anything has ever been pushed. A copy, not the internal slice
// itself, so a caller mutating the returned slice (or a later SetShowNames
// call reslicing s.showNames under the lock) can never race or corrupt
// the other's view, matching Snapshot's identical copy-out discipline.
func (s *fppConnectState) ShowNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.showNames == nil {
		return nil
	}
	out := make([]string, len(s.showNames))
	copy(out, s.showNames)
	return out
}

// SetShowNames replaces the held show name list.
func (s *fppConnectState) SetShowNames(names []string) {
	s.mu.Lock()
	s.showNames = names
	s.mu.Unlock()
}

// Settings returns the currently held fppconnect.settings and whether it
// has ever been pushed at all; before the first push there is no
// well-defined "held" value to return, unlike the coordinator's own
// resolveFPPConnectSettings, which always has a built-in default to fall
// back to.
func (s *fppConnectState) Settings() (fppConnectSettings, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings, s.settingsEverSet
}

// SetSettings replaces the held fppconnect.settings.
func (s *fppConnectState) SetSettings(v fppConnectSettings) {
	s.mu.Lock()
	s.settingsEverSet = true
	s.settings = v
	s.mu.Unlock()
}

// Snapshot copies every field of s into one value, used by
// [fppConnectState.Save] and by any caller (a future evidence report,
// tests) that needs a consistent, whole-state read rather than several
// separate locked reads that could interleave with a concurrent Apply.
func (s *fppConnectState) Snapshot() fppConnectSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, len(s.showNames))
	copy(names, s.showNames)
	return fppConnectSnapshot{
		ChannelRanges:     s.channelRanges,
		ActiveShowEverSet: s.activeShowEverSet,
		ActiveShowKnown:   s.activeShowKnown,
		ActiveShowName:    s.activeShowName,
		ShowNames:         names,
		SettingsEverSet:   s.settingsEverSet,
		Settings:          s.settings,
	}
}

// Apply replaces every field of s with snap's, in one lock: the shape
// "fppconnect.configure" (fppconnectops.go) uses to accept one push
// atomically, so a reader can never observe channel ranges from one push
// paired with an active show from another. Unlike SetChannelRanges, Apply
// does not itself re-validate ChannelRanges' length: fppconnectops.go's
// decode step already refuses an over-long value before Apply is ever
// called (see decodeFPPConnectConfigureParams), and Load's own caller
// (this same file) is restoring bytes this process already validated
// before persisting them.
func (s *fppConnectState) Apply(snap fppConnectSnapshot) {
	s.mu.Lock()
	s.channelRanges = snap.ChannelRanges
	s.activeShowEverSet = snap.ActiveShowEverSet
	s.activeShowKnown = snap.ActiveShowKnown
	s.activeShowName = snap.ActiveShowName
	s.showNames = snap.ShowNames
	s.settingsEverSet = snap.SettingsEverSet
	s.settings = snap.Settings
	s.mu.Unlock()
}

// fppConnectStateSubdir is where this node's FPP Connect state file lives,
// rooted at the agent's asset directory, matching internal/agent/
// heldcatalog.stateSubdir's identical convention of rooting local durable
// state under AssetDir rather than adding a second configured directory.
// enumerateAssets (assets.go) walks AssetDir non-recursively and skips
// every subdirectory, so a file kept here is never reported as a held
// asset.
const fppConnectStateSubdir = "fppconnect-state"

// fppConnectStateFileName is the single file [fppConnectState.Save] and
// [fppConnectState.Load] use.
const fppConnectStateFileName = "state.json"

func fppConnectStatePath(assetDir string) string {
	return filepath.Join(assetDir, fppConnectStateSubdir, fppConnectStateFileName)
}

// Save atomically persists s's current state under assetDir (temp file
// then rename). Unlike internal/agent/heldcatalog.FileStore.Save's fixed
// ".tmp" suffix, this uses [os.CreateTemp] to give every call its own
// uniquely-named temp file in the same directory: "fppconnect.configure"
// is handled in its own goroutine per inbound MQTT message
// (mqtt.go's "go cmdHandler.HandleMessage(...)"), so two pushes reaching
// this node close together (an ordinary occurrence, e.g. a "show" write
// and a "show.active" write both trigger their own push) call Save
// concurrently. A shared fixed tmp path under plain os.WriteFile (which
// truncates and writes, with no exclusivity between two writers) lets one
// goroutine's write interleave with another's rename, risking a truncated
// or mixed-content file landing at target; a private tmp file per call
// removes that interleaving entirely: the only remaining race is which
// completed Save's rename lands last, which is the ordinary "last write
// wins" outcome any concurrent write to one record has, not corruption.
func (s *fppConnectState) Save(assetDir string) error {
	return saveFPPConnectSnapshot(assetDir, s.Snapshot())
}

// saveFPPConnectSnapshot is [fppConnectState.Save]'s actual disk write,
// factored out to take an explicit snapshot rather than reading s's own
// current in-memory state, see [fppConnectConfigureOperation.configure]
// (fppconnectops.go), which persists a pushed snapshot to disk BEFORE
// applying it to the live holder, so a failed write never leaves
// in-memory state ahead of what a restart would actually recover.
func saveFPPConnectSnapshot(assetDir string, snap fppConnectSnapshot) error {
	dir := filepath.Join(assetDir, fppConnectStateSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fppconnect state: create state directory: %w", err)
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("fppconnect state: encode state: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, fppConnectStateFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("fppconnect state: create temp file: %w", err)
	}
	tmp := tmpFile.Name()
	_, writeErr := tmpFile.Write(data)
	closeErr := tmpFile.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fppconnect state: write state: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fppconnect state: close temp file: %w", closeErr)
	}

	target := fppConnectStatePath(assetDir)
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fppconnect state: commit state: %w", err)
	}
	return nil
}

// Load restores s from the file persisted under assetDir, replacing
// whatever s currently holds. ok is false when no state has ever been
// persisted (a fresh node); s is left unchanged in that case. A corrupt
// file (unreadable, or present but not decodable JSON) is reported as an
// error, never silently treated as "no state held", matching
// internal/agent/heldcatalog.FileStore.Load's identical "corrupt state is
// reported, never treated as absent" rule.
func (s *fppConnectState) Load(assetDir string) (ok bool, err error) {
	data, err := os.ReadFile(fppConnectStatePath(assetDir))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("fppconnect state: read state: %w", err)
	}
	var snap fppConnectSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return false, fmt.Errorf("fppconnect state: decode state: %w", err)
	}
	s.Apply(snap)
	return true, nil
}
