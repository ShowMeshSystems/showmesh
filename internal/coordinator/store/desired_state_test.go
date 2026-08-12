package store

import (
	"context"
	"errors"
	"testing"
)

// TestSetDesiredStateRoundTripsEachValueKind proves the reused
// encodeObservationValue/decodeObservationValue pair round-trips every
// supported kind through desired_state exactly as it already does through
// observations (observations_test.go covers that pair directly; this
// proves this table's own use of it is wired correctly).
func TestSetDesiredStateRoundTripsEachValueKind(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	cases := []struct {
		signal string
		value  any
	}{
		{"bool-signal", true},
		{"string-signal", "stop_playlist"},
		{"int-signal", int64(1<<62 + 1)},
		{"float-signal", 1920.0},
		{"nil-signal", nil},
	}
	for _, c := range cases {
		if _, err := st.SetDesiredState(ctx, DesiredStateRecord{
			ResourceKind: "fpp", ResourceID: "player-01", Signal: c.signal, Value: c.value, RequestedAt: st.now(),
		}); err != nil {
			t.Fatalf("set desired state %q: %v", c.signal, err)
		}
		got, err := st.GetDesiredState(ctx, "fpp", "player-01", c.signal)
		if err != nil {
			t.Fatalf("get desired state %q: %v", c.signal, err)
		}
		if got.Value != c.value {
			t.Errorf("signal %q: Value = %#v (%T), want %#v (%T)", c.signal, got.Value, got.Value, c.value, c.value)
		}
	}
}

// TestSetDesiredStateUpsertsReplacesPreviousValue proves the primary key
// is exactly (resource_kind, resource_id, signal): a second SetDesiredState
// for the same triple replaces the first, never adding a second row.
func TestSetDesiredStateUpsertsReplacesPreviousValue(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.SetDesiredState(ctx, DesiredStateRecord{
		ResourceKind: "fpp", ResourceID: "player-01", Signal: "player_state", Value: "playing", RequestedAt: st.now(),
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if _, err := st.SetDesiredState(ctx, DesiredStateRecord{
		ResourceKind: "fpp", ResourceID: "player-01", Signal: "player_state", Value: "stopped", RequestedAt: st.now(),
	}); err != nil {
		t.Fatalf("second set: %v", err)
	}

	all, err := st.ListDesiredState(ctx, DesiredStateFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want 1 (upsert, not append)", len(all))
	}
	if all[0].Value != "stopped" {
		t.Errorf("Value = %v, want %q (the second write)", all[0].Value, "stopped")
	}
}

// TestGetDesiredStateNotFound proves the sentinel error path for a triple
// nothing has ever requested.
func TestGetDesiredStateNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.GetDesiredState(context.Background(), "fpp", "no-such", "player_state"); !errors.Is(err, ErrDesiredStateNotFound) {
		t.Errorf("err = %v, want ErrDesiredStateNotFound", err)
	}
}

// TestDeleteDesiredStateRemovesRow proves deletion is distinct from
// setting a nil value (which is still a row — see [encodeObservationValue]'s
// valueKindNone case).
func TestDeleteDesiredStateRemovesRow(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.SetDesiredState(ctx, DesiredStateRecord{
		ResourceKind: "fpp", ResourceID: "player-01", Signal: "player_state", Value: "playing", RequestedAt: st.now(),
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := st.DeleteDesiredState(ctx, "fpp", "player-01", "player_state"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetDesiredState(ctx, "fpp", "player-01", "player_state"); !errors.Is(err, ErrDesiredStateNotFound) {
		t.Errorf("row still exists after delete: err = %v", err)
	}
}
