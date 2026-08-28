package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

func TestFPPConnectStateChannelRangesDefaultsEmpty(t *testing.T) {
	s := newFPPConnectState()
	if got := s.ChannelRanges(); got != "" {
		t.Errorf("ChannelRanges() = %q, want empty", got)
	}
}

// TestSetChannelRangesRefusesOverlongValue is the regression test for the
// silent-discovery-death bug: before this check, a ranges string over
// multisync.MaxPingRangesLength bytes was accepted here and only failed
// much later, inside EncodePing, on every subsequent discover-ping reply,
// so the node stopped answering discovery entirely while multiSyncStatus
// still reported listening.
func TestSetChannelRangesRefusesOverlongValue(t *testing.T) {
	s := newFPPConnectState()
	if err := s.SetChannelRanges("0-100"); err != nil {
		t.Fatalf("SetChannelRanges(valid) unexpected error: %v", err)
	}

	overlong := strings.Repeat("9", multisync.MaxPingRangesLength+1)
	err := s.SetChannelRanges(overlong)
	if !errors.Is(err, ErrChannelRangesTooLong) {
		t.Fatalf("SetChannelRanges(%d bytes) error = %v, want errors.Is(err, ErrChannelRangesTooLong)", len(overlong), err)
	}
	if got := s.ChannelRanges(); got != "0-100" {
		t.Fatalf("ChannelRanges() after a refused overlong SetChannelRanges = %q, want the previous value %q", got, "0-100")
	}
}

func TestFPPConnectStateActiveShowNeverSet(t *testing.T) {
	s := newFPPConnectState()
	name, known, ever := s.ActiveShow()
	if ever {
		t.Errorf("ever = true before any push, want false")
	}
	if known || name != "" {
		t.Errorf("ActiveShow() = (%q, %v), want (\"\", false) before any push", name, known)
	}
}

func TestFPPConnectStateSetActiveShowNilIsExplicitNoShow(t *testing.T) {
	s := newFPPConnectState()
	s.SetActiveShow(nil)
	name, known, ever := s.ActiveShow()
	if !ever {
		t.Fatal("ever = false after SetActiveShow(nil), want true")
	}
	if known {
		t.Error("known = true after SetActiveShow(nil), want false")
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
}

func TestFPPConnectStateSetActiveShowName(t *testing.T) {
	s := newFPPConnectState()
	show := "Front Yard"
	s.SetActiveShow(&show)
	name, known, ever := s.ActiveShow()
	if !ever || !known {
		t.Fatalf("ActiveShow() ever=%v known=%v, want true, true", ever, known)
	}
	if name != "Front Yard" {
		t.Errorf("name = %q, want Front Yard", name)
	}
}

func TestFPPConnectStateShowNames(t *testing.T) {
	s := newFPPConnectState()
	if got := s.ShowNames(); len(got) != 0 {
		t.Errorf("ShowNames() = %v, want empty before any push", got)
	}
	s.SetShowNames([]string{"Front Yard", "Back Yard"})
	got := s.ShowNames()
	if len(got) != 2 || got[0] != "Front Yard" || got[1] != "Back Yard" {
		t.Errorf("ShowNames() = %v, want [Front Yard Back Yard]", got)
	}
}

// TestFPPConnectStateSetShowNamesCopiesTheCallersSlice proves the
// review-round-2 fix: mutating the slice passed to SetShowNames after the
// call returns must never change what the holder reports, matching
// ShowNames' and Snapshot's identical copy discipline one field over.
func TestFPPConnectStateSetShowNamesCopiesTheCallersSlice(t *testing.T) {
	s := newFPPConnectState()
	names := []string{"Front Yard", "Back Yard"}
	s.SetShowNames(names)

	names[0] = "Mutated After The Call"

	got := s.ShowNames()
	if len(got) != 2 || got[0] != "Front Yard" || got[1] != "Back Yard" {
		t.Errorf("ShowNames() = %v after mutating the caller's own slice, want [Front Yard Back Yard] unchanged", got)
	}
}

func TestFPPConnectStateSettings(t *testing.T) {
	s := newFPPConnectState()
	if _, ok := s.Settings(); ok {
		t.Error("Settings() ok = true before any push, want false")
	}
	s.SetSettings(fppConnectSettings{Enabled: true, MaxFileBytes: 100, MaxAssetDirBytes: 1000})
	got, ok := s.Settings()
	if !ok {
		t.Fatal("Settings() ok = false after SetSettings, want true")
	}
	if got.MaxFileBytes != 100 || got.MaxAssetDirBytes != 1000 || !got.Enabled {
		t.Errorf("Settings() = %+v, want {true 100 1000}", got)
	}
}

func TestFPPConnectStateSnapshotApplyRoundTrip(t *testing.T) {
	s := newFPPConnectState()
	if err := s.SetChannelRanges("0-149,300-449"); err != nil {
		t.Fatalf("SetChannelRanges: %v", err)
	}
	show := "Front Yard"
	s.SetActiveShow(&show)
	s.SetShowNames([]string{"Front Yard", "Back Yard"})
	s.SetSettings(fppConnectSettings{Enabled: false, MaxFileBytes: 5, MaxAssetDirBytes: 50})

	snap := s.Snapshot()

	restored := newFPPConnectState()
	restored.Apply(snap)

	if restored.ChannelRanges() != "0-149,300-449" {
		t.Errorf("ChannelRanges() = %q after Apply", restored.ChannelRanges())
	}
	name, known, ever := restored.ActiveShow()
	if !ever || !known || name != "Front Yard" {
		t.Errorf("ActiveShow() = (%q, %v, %v) after Apply, want (Front Yard, true, true)", name, known, ever)
	}
	settings, ok := restored.Settings()
	if !ok || settings.Enabled || settings.MaxFileBytes != 5 {
		t.Errorf("Settings() = (%+v, %v) after Apply", settings, ok)
	}
}

// TestFPPConnectStateSaveLoadRoundTripAcrossRestart proves a restart (a
// NEW state object, Load from the same directory) returns exactly what
// was pushed and saved before the restart.
func TestFPPConnectStateSaveLoadRoundTripAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	original := newFPPConnectState()
	if err := original.SetChannelRanges("0-149"); err != nil {
		t.Fatalf("SetChannelRanges: %v", err)
	}
	show := "Front Yard"
	original.SetActiveShow(&show)
	original.SetShowNames([]string{"Front Yard", "Back Yard"})
	original.SetSettings(fppConnectSettings{Enabled: true, MaxFileBytes: 2147483648, MaxAssetDirBytes: 21474836480})

	if err := original.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	restarted := newFPPConnectState()
	ok, err := restarted.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("Load() ok = false, want true after a Save")
	}

	if restarted.ChannelRanges() != "0-149" {
		t.Errorf("ChannelRanges() = %q after restart, want 0-149", restarted.ChannelRanges())
	}
	name, known, ever := restarted.ActiveShow()
	if !ever || !known || name != "Front Yard" {
		t.Errorf("ActiveShow() = (%q, %v, %v) after restart, want (Front Yard, true, true)", name, known, ever)
	}
	names := restarted.ShowNames()
	if len(names) != 2 || names[0] != "Front Yard" {
		t.Errorf("ShowNames() = %v after restart", names)
	}
	settings, settingsOK := restarted.Settings()
	if !settingsOK || settings.MaxFileBytes != 2147483648 || settings.MaxAssetDirBytes != 21474836480 {
		t.Errorf("Settings() = (%+v, %v) after restart", settings, settingsOK)
	}
}

// TestFPPConnectStateLoadFreshNodeReturnsFalse proves a node that has
// never had anything pushed to it (no state file at all) loads cleanly
// with ok=false, never an error.
func TestFPPConnectStateLoadFreshNodeReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	s := newFPPConnectState()
	ok, err := s.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ok {
		t.Error("Load() ok = true on a fresh node with no persisted state, want false")
	}
}

// TestFPPConnectStateLoadCorruptFileReportsError proves a corrupt state
// file is reported as an error, never silently treated as "nothing held".
func TestFPPConnectStateLoadCorruptFileReportsError(t *testing.T) {
	dir := t.TempDir()
	original := newFPPConnectState()
	if err := original.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(fppConnectStatePath(dir), []byte("not json"), 0o644); err != nil {
		t.Fatalf("corrupting state file: %v", err)
	}

	s := newFPPConnectState()
	_, err := s.Load(dir)
	if err == nil {
		t.Fatal("Load() err = nil for a corrupt state file, want an error")
	}
}

// TestFPPConnectStateSaveDoesNotAppearInAssetInventory proves the saved
// state file lives under a subdirectory enumerateAssets never recurses
// into, matching heldcatalog's identical guarantee.
func TestFPPConnectStateSaveDoesNotAppearInAssetInventory(t *testing.T) {
	dir := t.TempDir()
	s := newFPPConnectState()
	if err := s.SetChannelRanges("0-149"); err != nil {
		t.Fatalf("SetChannelRanges: %v", err)
	}
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	assets, complete, reason := enumerateAssets(dir, map[string]hashCacheEntry{}, time.Now)
	if !complete {
		t.Fatalf("enumerateAssets incomplete: %s", reason)
	}
	if len(assets) != 0 {
		t.Errorf("enumerateAssets found %d assets, want 0 (the state file must be skipped as a subdirectory entry)", len(assets))
	}
}

// TestFPPConnectStateConcurrentSaveNeverCorrupts proves N concurrent Save
// calls (the real shape "fppconnect.configure" runs under: HandleMessage
// dispatches each inbound command to its own goroutine) never leave a
// truncated or mixed-content file behind: every Save must either succeed
// with a file Load can decode, or fail outright, never commit a corrupt
// intermediate state.
func TestFPPConnectStateConcurrentSaveNeverCorrupts(t *testing.T) {
	dir := t.TempDir()

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := newFPPConnectState()
			if err := s.SetChannelRanges(fmt.Sprintf("0-%d", i)); err != nil {
				errs[i] = err
				return
			}
			errs[i] = s.Save(dir)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Save() %d: %v", i, err)
		}
	}

	restored := newFPPConnectState()
	ok, err := restored.Load(dir)
	if err != nil {
		t.Fatalf("Load after concurrent Save: %v", err)
	}
	if !ok {
		t.Fatal("Load() ok = false after concurrent Save, want true")
	}
}
