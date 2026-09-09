package config

import (
	"os"
	"testing"
)

func TestReadFPPMQTTPasswordAbsentFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	password, present, err := ReadFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("ReadFPPMQTTPassword: %v", err)
	}
	if present || password != "" {
		t.Fatalf(`ReadFPPMQTTPassword = (%q, %v), want ("", false) when the legacy file was never written`, password, present)
	}
}

func TestReadFPPMQTTPasswordReturnsExactBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fppMQTTSecretFilePath(dir), []byte("exact-bytes"), 0o600); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	password, present, err := ReadFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("ReadFPPMQTTPassword: %v", err)
	}
	if !present || password != "exact-bytes" {
		t.Fatalf(`ReadFPPMQTTPassword = (%q, %v), want ("exact-bytes", true)`, password, present)
	}
}

func TestReadFPPMQTTPasswordEmptyFileIsPresentWithEmptyValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fppMQTTSecretFilePath(dir), []byte(""), 0o600); err != nil {
		t.Fatalf("seed empty legacy file: %v", err)
	}
	password, present, err := ReadFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("ReadFPPMQTTPassword: %v", err)
	}
	if !present || password != "" {
		t.Fatalf(`ReadFPPMQTTPassword = (%q, %v), want ("", true): an empty file is present but has nothing to move`, password, present)
	}
}

func TestClearFPPMQTTPasswordRemovesFileAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(fppMQTTSecretFilePath(dir), []byte("s3cret"), 0o600); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	if err := ClearFPPMQTTPassword(dir); err != nil {
		t.Fatalf("ClearFPPMQTTPassword: %v", err)
	}
	if _, present, err := ReadFPPMQTTPassword(dir); err != nil || present {
		t.Fatalf("ReadFPPMQTTPassword after clear = (_, %v, %v), want (_, false, nil)", present, err)
	}

	// Clearing an already-clear file is not an error.
	if err := ClearFPPMQTTPassword(dir); err != nil {
		t.Fatalf("ClearFPPMQTTPassword (already clear): %v", err)
	}
}
