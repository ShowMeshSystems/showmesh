package fpp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
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

// fieldAbsentError is returned by a rawDoc field extractor SPECIFICALLY
// when the JSON key itself is not present in the document — never for a
// key that is present but carries a value this package could not decode
// (wrong type, explicit JSON null, a non-integral number where an integer
// was expected, and so on). The distinction matters beyond error-message
// wording: contract section 3.3's mode-governed absence
// (modeAbsenceReason) must apply only to a field that is genuinely
// missing, never to one that arrived and failed to decode — see that
// function's doc comment for the review finding this type exists to fix
// (a present-but-malformed field on a remote-mode host was previously
// misreported as "unsupported because this host does not report that
// field," which is false: the field WAS reported, just not decodably).
type fieldAbsentError struct {
	key string
}

func (e *fieldAbsentError) Error() string {
	return fmt.Sprintf("field %q not present in response", e.key)
}

func newFieldAbsentError(key string) error {
	return &fieldAbsentError{key: key}
}

// isFieldAbsent reports whether err (as returned by a rawDoc field
// extractor) means "the key was not present" specifically, as opposed to
// any other decode failure. See fieldAbsentError's doc comment.
func isFieldAbsent(err error) bool {
	var e *fieldAbsentError
	return errors.As(err, &e)
}

// isJSONNull reports whether raw is the JSON literal null, ignoring
// surrounding whitespace. Every field extractor below checks this
// explicitly and rejects it as a decode error, distinct from the key
// being absent: encoding/json treats null as a valid value for almost any
// Go type (a string, bool, or float64 destination is simply left at its
// zero value with NO error returned), which silently manufactures a
// plausible-looking measured value — 0 mA, false, an empty summary — for
// a field FPP (or, reaching the same decoders via the MQTT source, any
// publisher with rights on that topic) reported as explicitly unknown.
// Contract section 3.2's whole point is that a missing "ma" must never
// decode to a plausible 0; a present-but-null "ma" is the same hazard
// through a door the original per-key `ok` check alone does not close.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// stringField extracts a string-valued field. A real FPP's idle-state
// values include several fields that are legitimately empty strings
// (current_sequence, current_playlist.playlist) — an empty string decodes
// successfully here and is not treated as absent; the caller decides what
// an empty value means for its signal.
func (d rawDoc) stringField(key string) (string, error) {
	raw, ok := d[key]
	if !ok {
		return "", newFieldAbsentError(key)
	}
	if isJSONNull(raw) {
		return "", fmt.Errorf("field %q: value is JSON null", key)
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
		return false, newFieldAbsentError(key)
	}
	if isJSONNull(raw) {
		return false, fmt.Errorf("field %q: value is JSON null", key)
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
		return 0, newFieldAbsentError(key)
	}
	if isJSONNull(raw) {
		return 0, fmt.Errorf("field %q: value is JSON null", key)
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

// intField extracts an integer-valued field, tolerating a JSON number or a
// numeric string exactly like numberField (the same two-encoding hazard
// applies to Step 5's new integer signals: current_playlist.index/count,
// scheduler.nextPlaylist.scheduledStartTime, volume, and the
// /api/system/info disk byte counts are all genuine JSON numbers in the
// captures this package decodes, while repeat_mode and seconds_elapsed
// arrive as numeric strings in the same documents — see testdata's
// live_*_fppd_status.json). Unlike numberField, this rejects a
// non-integral value rather than silently truncating it: a field this
// package models as a count or a byte size that ever arrives non-integral
// is a wrong assumption about that field, not something to paper over by
// dropping the fractional part.
func (d rawDoc) intField(key string) (int64, error) {
	f, err := d.numberField(key)
	if err != nil {
		return 0, err
	}
	if f != math.Trunc(f) {
		return 0, fmt.Errorf("field %q: %v is not an integer", key, f)
	}
	return int64(f), nil
}

// nestedIntField extracts an integer field from a nested object, e.g.
// nestedIntField("current_playlist", "index"). See intField and
// nestedStringField.
func (d rawDoc) nestedIntField(outerKey, innerKey string) (int64, error) {
	nested, err := d.objectField(outerKey)
	if err != nil {
		return 0, err
	}
	return nested.intField(innerKey)
}

// boolFromNumberOrBool extracts a bool-valued field that a real FPP was
// captured encoding as the JSON NUMBER 1/0 rather than a JSON boolean:
// scheduler.enabled reads "scheduler":{"enabled":1,...} on every one of
// this package's live captures (see testdata/live_*_fppd_status.json),
// never as true/false. This tolerates either encoding, the same shape of
// hazard numberField already tolerates for FPP's numeric-vs-string fields.
func (d rawDoc) boolFromNumberOrBool(key string) (bool, error) {
	raw, ok := d[key]
	if !ok {
		return false, newFieldAbsentError(key)
	}
	if isJSONNull(raw) {
		return false, fmt.Errorf("field %q: value is JSON null", key)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f != 0, nil
	}
	return false, fmt.Errorf("field %q: expected a bool or a number", key)
}

// nestedBoolFromNumberOrBool extracts a bool field from a nested object via
// boolFromNumberOrBool, e.g. nestedBoolFromNumberOrBool("scheduler",
// "enabled").
func (d rawDoc) nestedBoolFromNumberOrBool(outerKey, innerKey string) (bool, error) {
	nested, err := d.objectField(outerKey)
	if err != nil {
		return false, err
	}
	return nested.boolFromNumberOrBool(innerKey)
}

// nestedBoolField extracts a strictly-typed bool field from a nested
// object, e.g. nestedBoolField("MQTT", "connected"). Unlike
// nestedBoolFromNumberOrBool, this does not tolerate a numeric encoding —
// use it only for a field never captured as anything but a genuine JSON
// bool (MQTT.configured/connected, in every capture this package decodes).
func (d rawDoc) nestedBoolField(outerKey, innerKey string) (bool, error) {
	nested, err := d.objectField(outerKey)
	if err != nil {
		return false, err
	}
	return nested.boolField(innerKey)
}

// arrayField extracts a field whose value is a JSON array, returning each
// element as an undecoded json.RawMessage so the caller can decode entries
// independently (one malformed sensor or port element must not fail every
// other element — the same per-field independence decode.go applies
// everywhere else).
func (d rawDoc) arrayField(key string) ([]json.RawMessage, error) {
	raw, ok := d[key]
	if !ok {
		return nil, newFieldAbsentError(key)
	}
	if isJSONNull(raw) {
		return nil, fmt.Errorf("field %q: value is JSON null", key)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("field %q: expected a JSON array: %w", key, err)
	}
	return arr, nil
}

// normalizeKey derives a SignalID-safe key segment from an FPP-supplied
// name: lowercase, every run of characters outside [a-z0-9] collapsed to a
// single underscore, leading and trailing underscores trimmed. Used for
// both port element names ("Port 1" -> "port_1", contract section 3.1) and
// sensor labels ("K16-Max: " -> "k16_max", contract section 3.5) — both are
// free-text FPP fields with no guaranteed charset, and this is the one
// rule this package applies to either.
func normalizeKey(name string) string {
	var b strings.Builder
	lastWasUnderscore := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasUnderscore = false
		case !lastWasUnderscore:
			b.WriteByte('_')
			lastWasUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// objectField extracts a nested JSON object as another rawDoc, so its
// fields can in turn be extracted independently (e.g.
// current_playlist.playlist, scheduler.status).
func (d rawDoc) objectField(key string) (rawDoc, error) {
	raw, ok := d[key]
	if !ok {
		return nil, newFieldAbsentError(key)
	}
	if isJSONNull(raw) {
		return nil, fmt.Errorf("field %q: value is JSON null", key)
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
