package audio

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestFieldJSONThreeStates(t *testing.T) {
	type payload struct {
		Gain Field[Gain] `json:"gain"`
	}

	var omitted payload
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatalf("unmarshal omitted: %v", err)
	}
	if !omitted.Gain.IsUnset() {
		t.Errorf("omitted key: got state %v, want FieldUnset", omitted.Gain.State())
	}

	var explicitNull payload
	if err := json.Unmarshal([]byte(`{"gain": null}`), &explicitNull); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if !explicitNull.Gain.IsNull() {
		t.Errorf("explicit null: got state %v, want FieldNull", explicitNull.Gain.State())
	}

	var provided payload
	if err := json.Unmarshal([]byte(`{"gain": 0.75}`), &provided); err != nil {
		t.Fatalf("unmarshal value: %v", err)
	}
	if !provided.Gain.IsSet() {
		t.Errorf("provided value: got state %v, want FieldSet", provided.Gain.State())
	}
	v, ok := provided.Gain.Value()
	if !ok || v != 0.75 {
		t.Errorf("provided value: got (%v, %v), want (0.75, true)", v, ok)
	}
}

// TestFieldMarshalRoundTrip marshals a struct carrying a Field in each of
// the three states and unmarshals the result back, so a MarshalJSON that
// turns FieldUnset into "leave unchanged on decode" (i.e. null) is
// caught by round-tripping rather than only inspecting marshal output.
func TestFieldMarshalRoundTrip(t *testing.T) {
	type payload struct {
		Gain Field[Gain] `json:"gain"`
	}

	set := payload{Gain: SetField(Gain(0.5))}
	b, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	var decodedSet payload
	if err := json.Unmarshal(b, &decodedSet); err != nil {
		t.Fatalf("unmarshal round trip of set: %v", err)
	}
	if !decodedSet.Gain.IsSet() {
		t.Fatalf("round trip of set: got state %v, want FieldSet", decodedSet.Gain.State())
	}
	if v, _ := decodedSet.Gain.Value(); v != 0.5 {
		t.Fatalf("round trip of set: got %v, want 0.5", v)
	}

	null := payload{Gain: NullField[Gain]()}
	b, err = json.Marshal(null)
	if err != nil {
		t.Fatalf("marshal null: %v", err)
	}
	var decodedNull payload
	if err := json.Unmarshal(b, &decodedNull); err != nil {
		t.Fatalf("unmarshal round trip of null: %v", err)
	}
	if !decodedNull.Gain.IsNull() {
		t.Fatalf("round trip of null: got state %v, want FieldNull", decodedNull.Gain.State())
	}

	unset := payload{Gain: UnsetField[Gain]()}
	if _, err := json.Marshal(unset); err == nil {
		t.Fatal("marshal unset field: got nil error, want ErrFieldUnsetMarshal")
	} else if !errors.Is(err, ErrFieldUnsetMarshal) {
		t.Fatalf("marshal unset field: got %v, want ErrFieldUnsetMarshal", err)
	}
}

func TestFieldValueOnUnsetOrNullReturnsFalse(t *testing.T) {
	if _, ok := UnsetField[Gain]().Value(); ok {
		t.Error("Value() on unset field: got ok=true, want false")
	}
	if _, ok := NullField[Gain]().Value(); ok {
		t.Error("Value() on null field: got ok=true, want false")
	}
}
