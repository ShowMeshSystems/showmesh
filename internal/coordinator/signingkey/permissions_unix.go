//go:build unix

package signingkey

import (
	"fmt"
	"os"
	"syscall"
)

// checkFilePermissions verifies that path's mode carries no group or
// other permission bits (ADR-025 decision 4's coordinator-side intent —
// see [filePerm]'s doc comment) and that its owning uid matches this
// process's own (os.Getuid()): the coordinator is both the writer and the
// only legitimate reader of its own signing key, so an owner mismatch
// means something else on this host now controls a file this process
// trusts. A stat failure is reported, never silently treated as "fine."
func checkFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat key file: %w", err)
	}

	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("key file mode %s grants group or other access; want 0600 or stricter", info.Mode().Perm())
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("could not read the owning uid for the key file (unexpected FileInfo.Sys() type %T)", info.Sys())
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("key file is owned by uid %d, not this process's uid %d", stat.Uid, os.Getuid())
	}

	return nil
}
