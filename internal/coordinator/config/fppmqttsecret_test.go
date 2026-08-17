package config

import "testing"

func TestFPPMQTTPasswordFileRoundTrip(t *testing.T) {
	dir := t.TempDir()

	present, err := HasFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword: %v", err)
	}
	if present {
		t.Fatalf("HasFPPMQTTPassword = true before anything was written, want false")
	}
	if _, present, err := ReadFPPMQTTPassword(dir); err != nil || present {
		t.Fatalf("ReadFPPMQTTPassword = (_, %v, %v), want (_, false, nil) before anything was written", present, err)
	}

	if err := WriteFPPMQTTPassword(dir, "s3cret"); err != nil {
		t.Fatalf("WriteFPPMQTTPassword: %v", err)
	}
	present, err = HasFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword: %v", err)
	}
	if !present {
		t.Fatalf("HasFPPMQTTPassword = false after writing, want true")
	}
	got, present, err := ReadFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("ReadFPPMQTTPassword: %v", err)
	}
	if !present || got != "s3cret" {
		t.Fatalf("ReadFPPMQTTPassword = (%q, %v), want (\"s3cret\", true)", got, present)
	}

	// Overwrite with a new value.
	if err := WriteFPPMQTTPassword(dir, "rotated"); err != nil {
		t.Fatalf("WriteFPPMQTTPassword (rotate): %v", err)
	}
	got, present, err = ReadFPPMQTTPassword(dir)
	if err != nil || !present || got != "rotated" {
		t.Fatalf("ReadFPPMQTTPassword after rotation = (%q, %v, %v), want (\"rotated\", true, nil)", got, present, err)
	}

	if err := ClearFPPMQTTPassword(dir); err != nil {
		t.Fatalf("ClearFPPMQTTPassword: %v", err)
	}
	present, err = HasFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword after clear: %v", err)
	}
	if present {
		t.Fatalf("HasFPPMQTTPassword = true after clear, want false")
	}

	// Clearing an already-clear password is not an error.
	if err := ClearFPPMQTTPassword(dir); err != nil {
		t.Fatalf("ClearFPPMQTTPassword (already clear): %v", err)
	}
}

func TestFPPMQTTPasswordFileNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFPPMQTTPassword(dir, "exact-bytes"); err != nil {
		t.Fatalf("WriteFPPMQTTPassword: %v", err)
	}
	got, _, err := ReadFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("ReadFPPMQTTPassword: %v", err)
	}
	if got != "exact-bytes" {
		t.Fatalf("ReadFPPMQTTPassword = %q, want exactly \"exact-bytes\" with no added newline", got)
	}
}
