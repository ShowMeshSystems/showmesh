package capability

import (
	"errors"
	"testing"
)

func TestCapabilityValidate(t *testing.T) {
	tests := []struct {
		name    string
		c       Capability
		wantErr error // checked with errors.Is; nil means success
	}{
		{
			name: "valid, no attributes",
			c:    Capability{ID: "display.hdmi", Version: 1},
		},
		{
			name: "valid, with attributes",
			c: Capability{ID: "matrix.render", Version: 1, Attributes: map[string]any{
				"max_width": 1920, "max_height": 1080, "tested_fps": 40,
			}},
		},
		{
			name: "unknown but well-formed ID is valid",
			c:    Capability{ID: "widget.frobnicate", Version: 1},
		},
		{
			name:    "invalid ID syntax",
			c:       Capability{ID: "Matrix.Render", Version: 1},
			wantErr: ErrInvalidID,
		},
		{
			name:    "zero version",
			c:       Capability{ID: "matrix.render", Version: 0},
			wantErr: ErrInvalidVersion,
		},
		{
			name:    "negative version",
			c:       Capability{ID: "matrix.render", Version: -1},
			wantErr: ErrInvalidVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

func TestSetLookup(t *testing.T) {
	s := Set{
		{ID: "matrix.render", Version: 1},
		{ID: "display.hdmi", Version: 1, Attributes: map[string]any{"outputs": 2}},
	}

	if c, ok := s.Lookup("display.hdmi"); !ok || c.Version != 1 {
		t.Fatalf("Lookup(display.hdmi) = %v, %v, want a match", c, ok)
	}
	if _, ok := s.Lookup("audio.engine"); ok {
		t.Fatalf("Lookup(audio.engine) = true, want false: not a member of the set")
	}
}

func TestSetValidate(t *testing.T) {
	tests := []struct {
		name    string
		s       Set
		wantErr error
	}{
		{
			name: "empty set is valid",
			s:    Set{},
		},
		{
			name: "distinct valid capabilities",
			s: Set{
				{ID: "matrix.render", Version: 1},
				{ID: "display.hdmi", Version: 1},
			},
		},
		{
			name: "one invalid capability",
			s: Set{
				{ID: "matrix.render", Version: 1},
				{ID: "Bad.ID", Version: 1},
			},
			wantErr: ErrInvalidID,
		},
		{
			name: "duplicate ID rejected",
			s: Set{
				{ID: "matrix.render", Version: 1},
				{ID: "matrix.render", Version: 2},
			},
			wantErr: ErrDuplicateCapabilityID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestSetCanonicalJSONStable builds the same logical set many times over,
// alternating the order the three capabilities are appended to the Set
// between forward and fully reversed, and asserts every encoding is
// byte-for-byte identical. It does NOT vary the order any Attributes map is
// populated in: attrsA and attrsB below are identical map literals on every
// iteration, and Go's own encoding/json already sorts map keys on marshal
// regardless of insertion order, so that dimension needs no test here. What
// this test actually exercises is CanonicalJSON's own sort over Set's slice
// order.
func TestSetCanonicalJSONStable(t *testing.T) {
	build := func(reversed bool) Set {
		attrsA := map[string]any{"max_width": 1920, "max_height": 1080, "tested_fps": 40}
		attrsB := map[string]any{"outputs": 2, "device": "hdmi0"}

		a := Capability{ID: "matrix.render", Version: 1, Attributes: attrsA}
		b := Capability{ID: "display.hdmi", Version: 1, Attributes: attrsB}
		c := Capability{ID: "transport.ndi.send", Version: 1}

		if reversed {
			return Set{c, b, a}
		}
		return Set{a, b, c}
	}

	var want []byte
	for i := 0; i < 50; i++ {
		got, err := build(i%2 == 0).CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON() error = %v", err)
		}
		if want == nil {
			want = got
			continue
		}
		if string(got) != string(want) {
			t.Fatalf("CanonicalJSON() not stable across iteration %d:\n got  = %s\n want = %s", i, got, want)
		}
	}
}

func TestSetCanonicalJSONSortsByID(t *testing.T) {
	s := Set{
		{ID: "transport.ndi.send", Version: 1},
		{ID: "display.hdmi", Version: 1},
		{ID: "audio.engine", Version: 1},
	}

	got, err := s.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}

	want := `[{"id":"audio.engine","version":1},{"id":"display.hdmi","version":1},{"id":"transport.ndi.send","version":1}]`
	if string(got) != want {
		t.Fatalf("CanonicalJSON() =\n  %s\nwant\n  %s", got, want)
	}
}

// TestSetCanonicalJSONStableAcrossDuplicateIDOrder proves sorting by ID
// alone is not enough for a set containing a duplicate ID: Set{v1,v2} and
// Set{v2,v1} share the same multiset of IDs but arrive in opposite order,
// and a comparator that never says one is "less" than the other for equal
// IDs, combined with a stable sort, would preserve each input's own order
// and produce two different byte strings for what CanonicalJSON's doc
// comment promises is "the same set, reordered". CanonicalJSON does not
// reject the duplicate itself ([Set.Validate] does), but its ordering must
// not depend on which one came first.
func TestSetCanonicalJSONStableAcrossDuplicateIDOrder(t *testing.T) {
	v1 := Capability{ID: "a.b", Version: 1}
	v2 := Capability{ID: "a.b", Version: 2}

	forward, err := (Set{v1, v2}).CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	reversed, err := (Set{v2, v1}).CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}

	if string(forward) != string(reversed) {
		t.Fatalf("CanonicalJSON() not stable across duplicate-ID order:\n forward  = %s\n reversed = %s", forward, reversed)
	}
}
