//go:build unix

package signingkey

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFilePermissions_CorrectlyRestrictedFilePasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("key material"), filePerm); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	if err := checkFilePermissions(path); err != nil {
		t.Fatalf("checkFilePermissions() = %v, want nil", err)
	}
}

func TestCheckFilePermissions_GroupOrOtherAccessIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("key material"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	if err := checkFilePermissions(path); err == nil {
		t.Fatal("checkFilePermissions() = nil, want an error naming the loosened permissions")
	}
}

func TestCheckFilePermissions_MissingFileIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")

	err := checkFilePermissions(path)
	if err == nil {
		t.Fatal("checkFilePermissions() = nil, want an error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkFilePermissions() = %v, want it to wrap os.ErrNotExist", err)
	}
}
