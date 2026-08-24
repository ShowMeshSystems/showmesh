package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRecordFPPInstanceUUIDObservationFirstSighting proves a brand-new
// endpoint's first reported uuid is recorded with no conflict, there is
// nothing yet to compare it against.
func TestRecordFPPInstanceUUIDObservationFirstSighting(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	observedAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	rec, changed, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", observedAt)
	if err != nil {
		t.Fatalf("RecordFPPInstanceUUIDObservation: %v", err)
	}
	if changed {
		t.Errorf("changed = true on first sighting, want false")
	}
	if rec.UUID != "uuid-a" {
		t.Errorf("UUID = %q, want uuid-a", rec.UUID)
	}
	if rec.HasUnacknowledgedChange() {
		t.Errorf("HasUnacknowledgedChange() = true on first sighting, want false")
	}
	if !rec.FirstObservedAt.Equal(observedAt) || !rec.LastObservedAt.Equal(observedAt) {
		t.Errorf("First/LastObservedAt = %v/%v, want both %v", rec.FirstObservedAt, rec.LastObservedAt, observedAt)
	}
}

// TestRecordFPPInstanceUUIDObservationSameUUIDNoConflict proves
// re-observing the SAME uuid never raises a conflict and only advances
// LastObservedAt.
func TestRecordFPPInstanceUUIDObservationSameUUIDNoConflict(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	t1 := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", t1); err != nil {
		t.Fatalf("first RecordFPPInstanceUUIDObservation: %v", err)
	}
	rec, changed, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", t2)
	if err != nil {
		t.Fatalf("second RecordFPPInstanceUUIDObservation: %v", err)
	}
	if changed {
		t.Errorf("changed = true re-observing the same uuid, want false")
	}
	if rec.HasUnacknowledgedChange() {
		t.Errorf("HasUnacknowledgedChange() = true re-observing the same uuid, want false")
	}
	if !rec.LastObservedAt.Equal(t2) {
		t.Errorf("LastObservedAt = %v, want %v", rec.LastObservedAt, t2)
	}
	if !rec.FirstObservedAt.Equal(t1) {
		t.Errorf("FirstObservedAt = %v, want unchanged %v", rec.FirstObservedAt, t1)
	}
}

// TestRecordFPPInstanceUUIDObservationChangeIsVisibleNotSilent is the changed-uuid rule:
// an endpoint reporting a DIFFERENT uuid than it last reported (the SD
// card clone / restored backup / ADR-025 case) must never be a silent
// re-association. The new uuid becomes current immediately (this
// endpoint's other evidence must not go stale over a uuid disagreement),
// but the change stays visible as an unacknowledged conflict until an
// operator explicitly acknowledges it.
func TestRecordFPPInstanceUUIDObservationChangeIsVisibleNotSilent(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	t1 := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", t1); err != nil {
		t.Fatalf("first RecordFPPInstanceUUIDObservation: %v", err)
	}
	rec, changed, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-b", t2)
	if err != nil {
		t.Fatalf("second RecordFPPInstanceUUIDObservation: %v", err)
	}
	if !changed {
		t.Errorf("changed = false on a genuine uuid change, want true")
	}
	if rec.UUID != "uuid-b" {
		t.Errorf("UUID = %q, want uuid-b (the endpoint's current report)", rec.UUID)
	}
	if !rec.HasUnacknowledgedChange() {
		t.Fatalf("HasUnacknowledgedChange() = false after a uuid change, want true")
	}
	if rec.PreviousUUID != "uuid-a" {
		t.Errorf("PreviousUUID = %q, want uuid-a", rec.PreviousUUID)
	}
	if !rec.ChangedAt.Equal(t2) {
		t.Errorf("ChangedAt = %v, want %v", rec.ChangedAt, t2)
	}

	// Confirm a fresh read agrees, this is durable, not merely the
	// in-memory return value of the write call.
	reread, err := st.GetFPPInstanceUUID(ctx, "front-yard")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUID: %v", err)
	}
	if !reread.HasUnacknowledgedChange() || reread.PreviousUUID != "uuid-a" {
		t.Errorf("reread record lost its unacknowledged change: %+v", reread)
	}
}

// TestAcknowledgeFPPInstanceUUIDChangeClearsConflict proves the only way
// to clear a pending change is the explicit operator action, and that
// doing so records who and when without altering the current uuid.
func TestAcknowledgeFPPInstanceUUIDChangeClearsConflict(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	t1 := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", t1); err != nil {
		t.Fatalf("first RecordFPPInstanceUUIDObservation: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-b", t2); err != nil {
		t.Fatalf("second RecordFPPInstanceUUIDObservation: %v", err)
	}

	rec, err := st.AcknowledgeFPPInstanceUUIDChange(ctx, "front-yard", "principal-1", "Eric")
	if err != nil {
		t.Fatalf("AcknowledgeFPPInstanceUUIDChange: %v", err)
	}
	if rec.HasUnacknowledgedChange() {
		t.Errorf("HasUnacknowledgedChange() = true after acknowledgment, want false")
	}
	if rec.UUID != "uuid-b" {
		t.Errorf("UUID = %q after acknowledgment, want uuid-b (acknowledgment must not touch the current uuid)", rec.UUID)
	}
	if rec.ChangeAcknowledgedByPrincipalID != "principal-1" || rec.ChangeAcknowledgedByPrincipalName != "Eric" {
		t.Errorf("acknowledgment attribution = %q/%q, want principal-1/Eric",
			rec.ChangeAcknowledgedByPrincipalID, rec.ChangeAcknowledgedByPrincipalName)
	}

	// Acknowledging again with nothing pending refuses rather than
	// silently no-oping.
	if _, err := st.AcknowledgeFPPInstanceUUIDChange(ctx, "front-yard", "principal-1", "Eric"); !errors.Is(err, ErrFPPInstanceUUIDNoUnacknowledgedChange) {
		t.Errorf("re-acknowledge with nothing pending: err = %v, want ErrFPPInstanceUUIDNoUnacknowledgedChange", err)
	}
}

// TestAcknowledgeFPPInstanceUUIDChangeUnknownEndpoint proves acknowledging
// an endpoint that has never reported a uuid at all is refused with the
// more specific not-found error, not folded into the no-pending-change
// error.
func TestAcknowledgeFPPInstanceUUIDChangeUnknownEndpoint(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.AcknowledgeFPPInstanceUUIDChange(ctx, "nowhere", "principal-1", "Eric"); !errors.Is(err, ErrFPPInstanceUUIDNotFound) {
		t.Errorf("err = %v, want ErrFPPInstanceUUIDNotFound", err)
	}
}

// TestListFPPInstanceUUIDDuplicatesFindsSharedUUID is the duplicate-uuid rule: two
// endpoints reporting the SAME uuid must be a stated finding, never a
// silently overwritten row. Each endpoint keeps its own row, and the
// duplicate is reported as a group.
func TestListFPPInstanceUUIDDuplicatesFindsSharedUUID(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-shared", now); err != nil {
		t.Fatalf("record front-yard: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "back-yard", "uuid-shared", now.Add(time.Minute)); err != nil {
		t.Fatalf("record back-yard: %v", err)
	}
	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "garage", "uuid-unique", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("record garage: %v", err)
	}

	// Rule 2, made concrete: neither row was overwritten by the other.
	frontYard, err := st.GetFPPInstanceUUID(ctx, "front-yard")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUID front-yard: %v", err)
	}
	backYard, err := st.GetFPPInstanceUUID(ctx, "back-yard")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUID back-yard: %v", err)
	}
	if frontYard.UUID != "uuid-shared" || backYard.UUID != "uuid-shared" {
		t.Fatalf("both endpoints should independently retain uuid-shared, got %q and %q", frontYard.UUID, backYard.UUID)
	}

	dups, err := st.ListFPPInstanceUUIDDuplicates(ctx)
	if err != nil {
		t.Fatalf("ListFPPInstanceUUIDDuplicates: %v", err)
	}
	if len(dups) != 1 {
		t.Fatalf("len(dups) = %d, want 1: %+v", len(dups), dups)
	}
	if dups[0].UUID != "uuid-shared" {
		t.Errorf("dups[0].UUID = %q, want uuid-shared", dups[0].UUID)
	}
	if got, want := dups[0].EndpointIDs, []string{"back-yard", "front-yard"}; !equalStringSlices(got, want) {
		t.Errorf("dups[0].EndpointIDs = %v, want %v", got, want)
	}
}

// TestGetFPPInstanceUUIDByUUIDCorrelatesObservations proves the
// correlation direction TRACK-H's playlist-entry observations need: given
// an instanceUuid, find which configured endpoint(s) reported it.
func TestGetFPPInstanceUUIDByUUIDCorrelatesObservations(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	if _, _, err := st.RecordFPPInstanceUUIDObservation(ctx, "front-yard", "uuid-a", now); err != nil {
		t.Fatalf("record: %v", err)
	}

	matches, err := st.GetFPPInstanceUUIDByUUID(ctx, "uuid-a")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUIDByUUID: %v", err)
	}
	if len(matches) != 1 || matches[0].EndpointID != "front-yard" {
		t.Fatalf("matches = %+v, want exactly front-yard", matches)
	}

	noMatches, err := st.GetFPPInstanceUUIDByUUID(ctx, "uuid-never-seen")
	if err != nil {
		t.Fatalf("GetFPPInstanceUUIDByUUID (no match): %v", err)
	}
	if len(noMatches) != 0 {
		t.Errorf("noMatches = %+v, want empty", noMatches)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
