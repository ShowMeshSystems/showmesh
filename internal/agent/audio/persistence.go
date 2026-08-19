package audio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// sessionStateSubdir is where [FileSessionStore] keeps its files, rooted
// at the agent's asset directory — matching
// internal/agent/pipeline.AssignmentStore's identical convention of
// rooting local durable state under AssetDir rather than adding a second
// configured directory.
const sessionStateSubdir = "audio-sessions"

var sessionFileUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// FileSessionStore is a [SessionStore] backed by one JSON file per
// session, written atomically (temp file + rename). The session id
// travels inside the file's own content ([PersistedSession.ID]) because
// the filename is a lossy sanitization of it, not a reliable inverse.
type FileSessionStore struct {
	dir string
}

// NewFileSessionStore roots a store under assetDir.
func NewFileSessionStore(assetDir string) *FileSessionStore {
	return &FileSessionStore{dir: filepath.Join(assetDir, sessionStateSubdir)}
}

func (f *FileSessionStore) path(id pkgaudio.SessionID) string {
	name := sessionFileUnsafe.ReplaceAllString(string(id), "_")
	return filepath.Join(f.dir, name+".json")
}

// Save atomically replaces id's persisted record.
func (f *FileSessionStore) Save(id pkgaudio.SessionID, rec PersistedSession) error {
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return fmt.Errorf("audio: create session state directory: %w", err)
	}
	rec.ID = id
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("audio: encode session %s state: %w", id, err)
	}
	target := f.path(id)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("audio: write session %s state: %w", id, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("audio: commit session %s state: %w", id, err)
	}
	return nil
}

// Load returns id's persisted record, or (zero, false, nil) if none
// exists — a fresh node, or a session never persisted.
func (f *FileSessionStore) Load(id pkgaudio.SessionID) (PersistedSession, bool, error) {
	data, err := os.ReadFile(f.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return PersistedSession{}, false, nil
		}
		return PersistedSession{}, false, fmt.Errorf("audio: read session %s state: %w", id, err)
	}
	var rec PersistedSession
	if err := json.Unmarshal(data, &rec); err != nil {
		return PersistedSession{}, false, fmt.Errorf("audio: decode session %s state: %w", id, err)
	}
	return rec, true, nil
}

// Delete removes id's persisted record. Deleting an already-absent
// record is not an error.
func (f *FileSessionStore) Delete(id pkgaudio.SessionID) error {
	if err := os.Remove(f.path(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("audio: delete session %s state: %w", id, err)
	}
	return nil
}

// List returns every session id with a persisted, decodable record, for
// [Manager.RestoreAll]. A record whose file fails to decode is skipped
// here, not fatal to the rest of the listing — but it is never silently
// dropped: see [FileSessionStore.ListCorrupt], which the same walk feeds
// (see [FileSessionStore.ListCorrupt]'s doc). Skipping it here alone, with nothing else surfacing it,
// is indistinguishable from the session never having been persisted.
func (f *FileSessionStore) List() ([]pkgaudio.SessionID, error) {
	ids, _, err := f.walk()
	return ids, err
}

// ListCorrupt reports every persisted file [FileSessionStore.List]'s walk
// could not decode into a session id — an unreadable file, invalid JSON,
// or a record with no id — so a truncated write or disk corruption
// raises evidence instead of vanishing.
func (f *FileSessionStore) ListCorrupt() ([]CorruptSessionRecord, error) {
	_, corrupt, err := f.walk()
	return corrupt, err
}

// walk is List and ListCorrupt's shared directory scan, so the two views
// (decodable ids, and everything that was not) can never disagree about
// which files exist or drift out of sync with each other.
func (f *FileSessionStore) walk() ([]pkgaudio.SessionID, []CorruptSessionRecord, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("audio: list session state directory: %w", err)
	}
	var ids []pkgaudio.SessionID
	var corrupt []CorruptSessionRecord
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		if err != nil {
			corrupt = append(corrupt, CorruptSessionRecord{Filename: e.Name(), Reason: err.Error()})
			continue
		}
		var rec PersistedSession
		if err := json.Unmarshal(data, &rec); err != nil {
			corrupt = append(corrupt, CorruptSessionRecord{Filename: e.Name(), Reason: "invalid JSON: " + err.Error()})
			continue
		}
		if rec.ID == "" {
			corrupt = append(corrupt, CorruptSessionRecord{Filename: e.Name(), Reason: "record decoded with no session id"})
			continue
		}
		ids = append(ids, rec.ID)
	}
	return ids, corrupt, nil
}
