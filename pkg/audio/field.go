package audio

import (
	"encoding/json"
	"errors"
)

// FieldState is which of the three states a [Field] is in.
type FieldState int

const (
	// FieldUnset is a Field's zero value: the key was not mentioned at
	// all.
	FieldUnset FieldState = iota
	// FieldNull is an explicit JSON null: the caller means to clear
	// this field.
	FieldNull
	// FieldSet carries a real value the caller provided.
	FieldSet
)

// Field is a write-surface value that can be omitted (leave unchanged),
// explicitly null (clear it), or present (replace it).
type Field[T any] struct {
	state FieldState
	value T
}

// UnsetField returns a Field in the FieldUnset state.
func UnsetField[T any]() Field[T] { return Field[T]{state: FieldUnset} }

// NullField returns a Field in the FieldNull state.
func NullField[T any]() Field[T] { return Field[T]{state: FieldNull} }

// SetField returns a Field carrying v in the FieldSet state.
func SetField[T any](v T) Field[T] { return Field[T]{state: FieldSet, value: v} }

// State reports which of the three states f is in.
func (f Field[T]) State() FieldState { return f.state }

// IsUnset reports whether the field was not mentioned.
func (f Field[T]) IsUnset() bool { return f.state == FieldUnset }

// IsNull reports whether the field was explicitly cleared.
func (f Field[T]) IsNull() bool { return f.state == FieldNull }

// IsSet reports whether the field carries a real value.
func (f Field[T]) IsSet() bool { return f.state == FieldSet }

// Value returns f's value and true when f.IsSet, otherwise the zero T
// and false.
func (f Field[T]) Value() (T, bool) {
	if f.state != FieldSet {
		var zero T
		return zero, false
	}
	return f.value, true
}

// UnmarshalJSON implements the tri-state decode: called only when the
// key is present in the source object (an absent key leaves the zero,
// FieldUnset value untouched by encoding/json). A JSON null sets
// FieldNull; any other token decodes into T and sets FieldSet.
func (f *Field[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		var zero T
		f.state, f.value = FieldNull, zero
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	f.state, f.value = FieldSet, v
	return nil
}

// ErrFieldUnsetMarshal is returned by [Field.MarshalJSON] for a
// FieldUnset value: encoding it as null would turn "leave unchanged"
// into "clear it" on the wire. A caller that needs to omit an unset
// field builds its own optional-shaped payload rather than marshaling
// this type directly.
var ErrFieldUnsetMarshal = errors.New("audio: cannot marshal an unset field")

// MarshalJSON encodes FieldSet as the value's own JSON and FieldNull as
// JSON null. FieldUnset is [ErrFieldUnsetMarshal].
func (f Field[T]) MarshalJSON() ([]byte, error) {
	switch f.state {
	case FieldSet:
		return json.Marshal(f.value)
	case FieldNull:
		return []byte("null"), nil
	default:
		return nil, ErrFieldUnsetMarshal
	}
}
