package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFallbackProgramRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetFallbackProgram(ctx, "M4-1"); !errors.Is(err, ErrFallbackProgramNotFound) {
		t.Fatalf("GetFallbackProgram before any publish: got %v, want ErrFallbackProgramNotFound", err)
	}

	rec := FallbackProgramRecord{
		FPPInstanceUUID: "M4-1",
		PackageID:       "pkg-1",
		Revision:        "rev-1",
		ShowID:          "halloween",
		Generation:      2,
		ProgramJSON:     `{"schemaVersion":1}`,
		SignatureB64:    "c2ln",
		ExpiresAt:       time.Unix(2000, 0).UTC(),
		CompiledAt:      time.Unix(1000, 0).UTC(),
	}
	if err := st.PutFallbackProgram(ctx, rec); err != nil {
		t.Fatalf("PutFallbackProgram: %v", err)
	}

	got, err := st.GetFallbackProgram(ctx, "M4-1")
	if err != nil {
		t.Fatalf("GetFallbackProgram: %v", err)
	}
	if got.Revision != rec.Revision || got.PackageID != rec.PackageID || got.ShowID != rec.ShowID {
		t.Fatalf("GetFallbackProgram = %+v, want %+v", got, rec)
	}
	if !got.ExpiresAt.Equal(rec.ExpiresAt) || !got.CompiledAt.Equal(rec.CompiledAt) {
		t.Fatalf("GetFallbackProgram times = %+v, want %+v", got, rec)
	}

	// A second publish upserts, wholesale, never merges.
	rec.PackageID = "pkg-2"
	rec.Revision = "rev-2"
	if err := st.PutFallbackProgram(ctx, rec); err != nil {
		t.Fatalf("PutFallbackProgram (second): %v", err)
	}
	got, err = st.GetFallbackProgram(ctx, "M4-1")
	if err != nil {
		t.Fatalf("GetFallbackProgram (after upsert): %v", err)
	}
	if got.Revision != "rev-2" || got.PackageID != "pkg-2" {
		t.Fatalf("GetFallbackProgram after upsert = %+v, want revision rev-2, packageId pkg-2", got)
	}

	list, err := st.ListFallbackPrograms(ctx)
	if err != nil {
		t.Fatalf("ListFallbackPrograms: %v", err)
	}
	if len(list) != 1 || list[0].FPPInstanceUUID != "M4-1" {
		t.Fatalf("ListFallbackPrograms = %+v, want exactly one row for M4-1", list)
	}
}

func TestFallbackProgramAckRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetFallbackProgramAck(ctx, "M4-1"); !errors.Is(err, ErrFallbackProgramAckNotFound) {
		t.Fatalf("GetFallbackProgramAck before any ack: got %v, want ErrFallbackProgramAckNotFound", err)
	}

	rec := FallbackProgramAckRecord{
		FPPInstanceUUID:    "M4-1",
		PackageID:          "pkg-1",
		Revision:           "rev-1",
		VerificationResult: "verified",
		InstalledAt:        time.Unix(500, 0).UTC(),
		AcknowledgedAt:     time.Unix(600, 0).UTC(),
	}
	if err := st.PutFallbackProgramAck(ctx, rec); err != nil {
		t.Fatalf("PutFallbackProgramAck: %v", err)
	}

	got, err := st.GetFallbackProgramAck(ctx, "M4-1")
	if err != nil {
		t.Fatalf("GetFallbackProgramAck: %v", err)
	}
	if got.PackageID != rec.PackageID || got.Revision != rec.Revision || got.VerificationResult != rec.VerificationResult {
		t.Fatalf("GetFallbackProgramAck = %+v, want %+v", got, rec)
	}
}
