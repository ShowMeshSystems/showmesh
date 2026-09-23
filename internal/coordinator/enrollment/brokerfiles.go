package enrollment

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// The file names deploy/mosquitto/generate-credentials.sh reads and writes.
const (
	passwdFileName       = "passwd"
	aclBaseFileName      = "acl.conf"
	aclGeneratedFileName = "acl.generated.conf"
	aclMigrationMarker   = ".acl-explicit-agents-v1"
)

// File modes the coordinator writes. The broker reads both through the
// group the config directory hands down (see deploy/README.md).
const (
	passwdFileMode       fs.FileMode = 0o640
	aclGeneratedFileMode fs.FileMode = 0o644
)

// ErrBrokerFilesUnavailable wraps every reason the coordinator cannot
// manage built-in broker logins.
var ErrBrokerFilesUnavailable = errors.New("enrollment: broker login files unavailable")

// BrokerFiles writes the built-in broker's password file and generated
// access list in one directory.
type BrokerFiles struct {
	dir string
	mu  sync.Mutex
}

// NewBrokerFiles returns a BrokerFiles over dir. An empty dir is allowed
// and reported by [BrokerFiles.Check].
func NewBrokerFiles(dir string) *BrokerFiles { return &BrokerFiles{dir: dir} }

func (b *BrokerFiles) path(name string) string { return filepath.Join(b.dir, name) }

// brokerFilesError carries an operator-facing message and matches
// [ErrBrokerFilesUnavailable].
type brokerFilesError struct{ msg string }

func (e *brokerFilesError) Error() string        { return e.msg }
func (e *brokerFilesError) Is(target error) bool { return target == ErrBrokerFilesUnavailable }

func unavailable(format string, args ...any) error {
	return &brokerFilesError{msg: fmt.Sprintf(format, args...)}
}

// Check reports why the coordinator cannot write broker logins, or nil.
func (b *BrokerFiles) Check() error {
	if b.dir == "" {
		return unavailable("The coordinator cannot manage broker logins because SHOWMESH_BROKER_CONFIG_DIR is not set. Set it to the broker's config directory, mounted read-write, and restart the coordinator.")
	}
	for _, name := range []string{passwdFileName, aclBaseFileName} {
		f, err := os.Open(b.path(name))
		if err != nil {
			return unavailable("The coordinator cannot read %s. Run deploy/mosquitto/generate-credentials.sh and check the directory's owner and mode, then try again.", b.path(name))
		}
		_ = f.Close()
	}
	if _, err := os.Stat(b.path(aclMigrationMarker)); err != nil {
		return unavailable("The broker access list has not been migrated in %s. Run deploy/mosquitto/generate-credentials.sh --migrate-existing, then try again.", b.dir)
	}
	probe, err := os.CreateTemp(b.dir, ".showmesh-write-check-")
	if err != nil {
		return unavailable("The coordinator cannot write to %s. Mount it read-write and give the coordinator write access, then try again.", b.dir)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return nil
}

// HasUser reports whether the password file has a login named username.
func (b *BrokerFiles) HasUser(username string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	passwd, err := os.ReadFile(b.path(passwdFileName))
	if err != nil {
		return false, unavailable("The coordinator cannot read %s. Check the directory's owner and mode, then try again.", b.path(passwdFileName))
	}
	return passwdHasUser(passwd, username), nil
}

// Provision gives username a fresh password, rebuilds the generated access
// list, and returns the password and a function that puts both files back
// the way they were.
func (b *BrokerFiles) Provision(username string) (password string, undo func() error, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.Check(); err != nil {
		return "", nil, err
	}
	oldPasswd, err := os.ReadFile(b.path(passwdFileName))
	if err != nil {
		return "", nil, unavailable("The coordinator cannot read %s. Check the directory's owner and mode, then try again.", b.path(passwdFileName))
	}
	base, err := os.ReadFile(b.path(aclBaseFileName))
	if err != nil {
		return "", nil, unavailable("The coordinator cannot read %s. Check the directory's owner and mode, then try again.", b.path(aclBaseFileName))
	}
	oldACL, aclErr := os.ReadFile(b.path(aclGeneratedFileName))
	hadACL := aclErr == nil
	if aclErr != nil && !errors.Is(aclErr, fs.ErrNotExist) {
		return "", nil, unavailable("The coordinator cannot read %s. Check the directory's owner and mode, then try again.", b.path(aclGeneratedFileName))
	}

	password, err = randomBrokerPassword()
	if err != nil {
		return "", nil, err
	}
	entry, err := passwdEntry(username, password)
	if err != nil {
		return "", nil, err
	}
	newPasswd := upsertPasswdEntry(oldPasswd, username, entry)
	newACL, err := renderACL(base, newPasswd)
	if err != nil {
		return "", nil, err
	}

	if err := writeFileAtomic(b.dir, passwdFileName, newPasswd, passwdFileMode); err != nil {
		return "", nil, err
	}
	if err := writeFileAtomic(b.dir, aclGeneratedFileName, newACL, aclGeneratedFileMode); err != nil {
		restoreErr := writeFileAtomic(b.dir, passwdFileName, oldPasswd, passwdFileMode)
		return "", nil, errors.Join(err, restoreErr)
	}

	undo = func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		errPasswd := writeFileAtomic(b.dir, passwdFileName, oldPasswd, passwdFileMode)
		var errACL error
		if hadACL {
			errACL = writeFileAtomic(b.dir, aclGeneratedFileName, oldACL, aclGeneratedFileMode)
		} else if rmErr := os.Remove(b.path(aclGeneratedFileName)); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			errACL = rmErr
		}
		return errors.Join(errPasswd, errACL)
	}
	return password, undo, nil
}

// randomBrokerPassword matches the scripts' random_password: 24 random
// bytes, base64 with + and / replaced by - and _.
func randomBrokerPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("enrollment: generate broker password: %w", err)
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

// writeFileAtomic writes data to a temporary file in dir and renames it
// over name, so a reader sees the old file or the new one, never a mix.
func writeFileAtomic(dir, name string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-")
	if err != nil {
		return unavailable("The coordinator cannot write to %s. Mount it read-write and give the coordinator write access, then try again.", dir)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return unavailable("The coordinator could not write %s: %v", filepath.Join(dir, name), err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return unavailable("The coordinator could not set the mode of %s: %v", filepath.Join(dir, name), err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return unavailable("The coordinator could not write %s: %v", filepath.Join(dir, name), err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return unavailable("The coordinator could not write %s: %v", filepath.Join(dir, name), err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		cleanup()
		return unavailable("The coordinator could not replace %s: %v", filepath.Join(dir, name), err)
	}
	return nil
}
