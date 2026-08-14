package fppcommand

import (
	"errors"
	"strconv"
	"strings"
)

// ValidationError is the distinct, [errors.Is]/[errors.As]-able error type
// every validation rule in this file returns. Field names which typed
// helper's argument failed (e.g. "name", "volume"); Unwrap returns one of
// this file's sentinel errors, so a caller can test for a specific rule
// with errors.Is(err, ErrPlaylistNameSeparator) or recover the field with
// errors.As(err, &validationErr) without string-matching an error
// message.
//
// Error() deliberately returns just the underlying sentinel's own text
// (e.g. "playlist name is required"), not a "fppcommand: invalid X: ..."
// wrapper: this message reaches the operator verbatim, through
// [invalidParameterProblem]'s Detail field on the wire and the UI/CLI
// surfaces that render it, and every sentinel below already names its own
// field in plain English. A caller that also wants the package/field
// context programmatically should use errors.As, not string-parse Error().
type ValidationError struct {
	Field string
	Err   error
}

func (e *ValidationError) Error() string {
	return e.Err.Error()
}

func (e *ValidationError) Unwrap() error {
	return e.Err
}

// Sentinel errors for every validation rule this package enforces. Each
// is reachable via errors.Is against a *ValidationError this package
// returns. None of these is ever returned bare — always wrapped in a
// *ValidationError naming the field that failed.
var (
	// ErrPlaylistNameRequired: a playlist name argument is required and
	// was empty. Capture section 1.4: FPP itself answers a 200 "Playlist
	// is a requirement argument" for an empty name rather than rejecting
	// it, which is exactly the kind of "succeeded" response ADR-003
	// exists to not trust — this package refuses before dispatch instead.
	ErrPlaylistNameRequired = errors.New("playlist name is required")

	// ErrPlaylistNameWhitespace: the name carries leading or trailing
	// whitespace. Rejected rather than trimmed deliberately — the
	// operator's value is not this package's to edit.
	ErrPlaylistNameWhitespace = errors.New("playlist name has leading or trailing whitespace")

	// ErrPlaylistNameTooLong: the name exceeds maxPlaylistNameBytes bytes.
	// The message says 250, matching maxPlaylistNameBytes itself — an
	// earlier version of this text said 255 (the POSIX filename limit
	// this bound derives FROM, not the bound this package actually
	// enforces; see maxPlaylistNameBytes's own doc comment for why the
	// two differ), which told an operator hitting this a limit five
	// bytes wider than the one actually being applied.
	ErrPlaylistNameTooLong = errors.New("playlist name exceeds 250 bytes")

	// ErrPlaylistNameControlChar: the name contains an ASCII control
	// character (0x00-0x1F or 0x7F).
	ErrPlaylistNameControlChar = errors.New("playlist name contains an ASCII control character")

	// ErrPlaylistNameTraversal: the name contains "/", "\", or the
	// substring "..". FPP resolves a playlist name directly to
	// /home/fpp/media/playlists/{name}.json with no sanitization this
	// package has evidence of, so a name carrying a path separator is a
	// traversal ShowMesh must not be the mechanism for
	// (docs/bench/fpp-command-vocabulary.md section 1.3).
	ErrPlaylistNameTraversal = errors.New("playlist name must not contain \"/\", \"\\\", or \"..\"")

	// ErrVolumeOutOfRange: volume is outside 0..100 inclusive. Capture
	// section 1.5 measured FPP itself silently clamping 999 to 100 and
	// silently coercing "abc" to 0 rather than rejecting either — there is
	// no version of "let FPP reject it" that works for volume. The message
	// names both constraints (whole number, 0-100) together rather than
	// only the range: by the time this check runs the value is already a
	// decoded integer (a fractional value is refused earlier, by
	// decodeFPPParamValue in internal/coordinator/api), but stating both
	// gives the operator one complete, self-contained rule rather than a
	// range-only message that reads as though a decimal might otherwise be
	// fine.
	ErrVolumeOutOfRange = errors.New("volume must be a whole number from 0 to 100")
)

// maxPlaylistNameBytes is Step 8's own bound on a playlist name argument.
// SHOWMESH-CHOSEN, NOT MEASURED: the capture did not exercise FPP's own
// filesystem or filename length limits, so this is a deliberately
// conservative bound rather than one derived from an observed FPP
// rejection.
//
// 250, not 255, and the five bytes are the point. FPP resolves a playlist
// name to /home/fpp/media/playlists/{name}.json
// (docs/bench/fpp-command-vocabulary.md section 1.3), so the string that
// actually has to fit within a POSIX NAME_MAX of 255 is the name PLUS
// ".json". Bounding the name at 255 would let this package accept a name
// that cannot become a filename, and the failure would land on FPP as
// whatever FPP does with an over-long path — which the capture did not
// establish, and which section 2 gives every reason to expect is a
// cheerful 200. Bounding the input by the limit on a DERIVED value, rather
// than on the value itself, is the same class of error as validating a
// field and then dispatching a transformation of it.
const maxPlaylistNameBytes = 250

// ValidatePlaylistName validates a playlist name argument before it is
// ever sent to FPP. Every rule here exists because capture section 1.5
// proved FPP does not validate argument values, it coerces them, so
// there is no version of "let FPP reject it" that works — this package
// must refuse before dispatch or not at all.
//
// Rules, checked in this order:
//
//  1. Non-empty. See [ErrPlaylistNameRequired].
//  2. No leading or trailing whitespace — rejected, never trimmed. The
//     operator's value is not this package's to edit. See
//     [ErrPlaylistNameWhitespace].
//  3. At most 250 bytes. See [ErrPlaylistNameTooLong].
//  4. No ASCII control characters. See [ErrPlaylistNameControlChar].
//  5. Must not contain "/", "\", or the substring "..". FPP resolves a
//     playlist name to /home/fpp/media/playlists/{name}.json
//     (docs/bench/fpp-command-vocabulary.md section 1.3), so a name
//     carrying a path separator is a traversal ShowMesh must not be the
//     mechanism for. See [ErrPlaylistNameTraversal].
//
// A non-nil return is always a *ValidationError with Field "name",
// unwrapping to one of this file's sentinel errors.
func ValidatePlaylistName(name string) error {
	if name == "" {
		return &ValidationError{Field: "name", Err: ErrPlaylistNameRequired}
	}
	if strings.TrimSpace(name) != name {
		return &ValidationError{Field: "name", Err: ErrPlaylistNameWhitespace}
	}
	if len(name) > maxPlaylistNameBytes {
		return &ValidationError{Field: "name", Err: ErrPlaylistNameTooLong}
	}
	for i := 0; i < len(name); i++ {
		if b := name[i]; b < 0x20 || b == 0x7f {
			return &ValidationError{Field: "name", Err: ErrPlaylistNameControlChar}
		}
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return &ValidationError{Field: "name", Err: ErrPlaylistNameTraversal}
	}
	return nil
}

// ValidateVolume validates a volume argument before it is ever sent to
// FPP. Capture section 1.5 measured FPP itself silently clamping
// Volume Set/999 to 100 and silently coercing Volume Set/abc to 0 rather
// than rejecting either, so this package rejects an out-of-range value
// itself rather than ever letting FPP coerce one.
//
// A non-nil return is always a *ValidationError with Field "volume",
// unwrapping to [ErrVolumeOutOfRange].
func ValidateVolume(volume int) error {
	if volume < 0 || volume > 100 {
		return &ValidationError{Field: "volume", Err: ErrVolumeOutOfRange}
	}
	return nil
}

// encodeBool encodes a boolean command argument as exactly "true" or
// "false" — the only two values FPP's own parser recognizes. Read
// directly from FPP's PlaylistCommands.cpp
// (docs/bench/fpp-command-vocabulary.md section 1.5): a boolean argument
// is parsed as args[n] == "true" || args[n] == "1", and literally
// everything else — including "TRUE", "yes", and "" — is treated as
// false. strconv.FormatBool already produces exactly "true"/"false", so
// this function adds no behavior; it exists to name the fact at every
// call site rather than let a future edit reach for a different
// encoding (e.g. "1"/"0", which capture section 1.5 does NOT establish
// as accepted for false, only "1" as a truthy synonym).
func encodeBool(b bool) string {
	return strconv.FormatBool(b)
}
