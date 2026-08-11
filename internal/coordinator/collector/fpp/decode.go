package fpp

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// rawDoc is a JSON object decoded shallowly: each top-level key's value is
// kept as unparsed json.RawMessage rather than unmarshalled into a fixed
// struct. This is the mechanism behind the Task C spec's "make a decode
// failure of one field degrade that field rather than the whole document":
// unmarshalling a real captured /api/fppd/status body into one strict
// struct means a single field with an unexpected JSON type (see
// numberField's doc comment for exactly this happening in practice) fails
// the ENTIRE decode — a decoding bug that looks, from the outside, exactly
// like the FPP being unreachable. Decoding into rawDoc first can only fail
// if the body is not a JSON object at all; every field extraction after
// that is independent.
type rawDoc map[string]json.RawMessage

// decodeRawDoc parses body as a JSON object. The only failure mode is "this
// was not valid JSON" or "this was valid JSON but not an object" — both
// genuinely mean nothing in body can be trusted, unlike a single
// unexpected field type deeper in the document.
func decodeRawDoc(body []byte) (rawDoc, error) {
	var doc rawDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("response body is not a JSON object: %w", err)
	}
	return doc, nil
}

// stringField extracts a string-valued field. A real FPP's idle-state
// values include several fields that are legitimately empty strings
// (current_sequence, current_playlist.playlist) — an empty string decodes
// successfully here and is not treated as absent; the caller decides what
// an empty value means for its signal.
func (d rawDoc) stringField(key string) (string, error) {
	raw, ok := d[key]
	if !ok {
		return "", fmt.Errorf("field %q not present in response", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("field %q: expected a string: %w", key, err)
	}
	return s, nil
}

// boolField extracts a bool-valued field. Deliberately strict — FPP does
// not appear to render a boolean as a JSON string anywhere in this
// package's captured bodies, unlike its numeric fields (see numberField),
// so no tolerant fallback is added here without a captured body that
// actually needs one.
func (d rawDoc) boolField(key string) (bool, error) {
	raw, ok := d[key]
	if !ok {
		return false, fmt.Errorf("field %q not present in response", key)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, fmt.Errorf("field %q: expected a bool: %w", key, err)
	}
	return b, nil
}

// numberField extracts a numeric field from a real FPP 9.5.3 that was found
// to arrive inconsistently: "seconds_played", "seconds_remaining", and
// "repeat_mode" are JSON STRINGS ("0"), while "mode", "status", "volume",
// and "uptimeSeconds" are genuine JSON numbers, all in the same document
// (see testdata). A struct field typed as a Go number fails to unmarshal
// the string-typed ones outright — a decoding bug that, from the outside,
// wears a network fault's clothes: it takes the whole document down with
// it, and the collector reports the FPP unreachable when it was not. This
// method tries a JSON number first, then a JSON string containing a
// number, tolerating either FPP encoding without caring which one arrived.
func (d rawDoc) numberField(key string) (float64, error) {
	raw, ok := d[key]
	if !ok {
		return 0, fmt.Errorf("field %q not present in response", key)
	}

	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("field %q: not a JSON number or a numeric string", key)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q: numeric string %q: %w", key, s, err)
	}
	return f, nil
}

// objectField extracts a nested JSON object as another rawDoc, so its
// fields can in turn be extracted independently (e.g.
// current_playlist.playlist, scheduler.status).
func (d rawDoc) objectField(key string) (rawDoc, error) {
	raw, ok := d[key]
	if !ok {
		return nil, fmt.Errorf("field %q not present in response", key)
	}
	var nested rawDoc
	if err := json.Unmarshal(raw, &nested); err != nil {
		return nil, fmt.Errorf("field %q: expected a JSON object: %w", key, err)
	}
	return nested, nil
}

// nestedStringField extracts a string field from a nested object in one
// call, e.g. nestedStringField("current_playlist", "playlist"). If the
// outer key is missing or not an object, or the inner key is missing or
// not a string, the returned error names which part failed.
func (d rawDoc) nestedStringField(outerKey, innerKey string) (string, error) {
	nested, err := d.objectField(outerKey)
	if err != nil {
		return "", err
	}
	return nested.stringField(innerKey)
}

// multiSyncSystemsCount decodes the /api/fppd/multiSyncSystems response
// shape, verified against a real FPP 9.5.3 (see testdata):
//
//	{"Message":"","Status":"OK","respCode":200,"systems":[...]}
//
// and returns len(systems). This is intentionally its own small decoder
// rather than routed through rawDoc: the response is an envelope around an
// array, not the flat object /api/fppd/status returns, so reusing rawDoc's
// field extractors would not fit cleanly.
func multiSyncSystemsCount(body []byte) (int, error) {
	var envelope struct {
		Systems []json.RawMessage `json:"systems"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, fmt.Errorf("response body is not the expected multiSyncSystems envelope: %w", err)
	}
	return len(envelope.Systems), nil
}
