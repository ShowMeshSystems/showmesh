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

// List returns every session id with a persisted record, for
// [Manager.RestoreAll]. A record whose file fails to decode is skipped,
// not fatal to the rest of the listing — a caller sweeping every id
// still has [FileSessionStore.Load] surface that specific record's own
// error.
func (f *FileSessionStore) List() ([]pkgaudio.SessionID, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("audio: list session state directory: %w", err)
	}
	var ids []pkgaudio.SessionID
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		if err != nil {
			continue
		}
		var rec PersistedSession
		if err := json.Unmarshal(data, &rec); err != nil || rec.ID == "" {
			continue
		}
		ids = append(ids, rec.ID)
	}
	return ids, nil
}
