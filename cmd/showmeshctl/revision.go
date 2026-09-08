package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strconv"
)

// This file is every config-write command's shared If-Match support: the
// coordinator answers 409 on a stale If-Match (parseRevisionPrecondition,
// checkRevisionPrecondition, internal/coordinator/api/showconfig.go), and
// this makes every "<kind> set|put" command send one by default, so a
// script that reads an object, waits, and writes it back cannot silently
// overwrite whatever an operator changed to it in the meantime.
//
// Precedence, highest first: an explicit --if-match flag; a "revision"
// field the operator's own payload carried (only for a command that
// already parses a full round-trip payload, see wrapperRevision);
// otherwise a fresh GET of the same object immediately before the PUT.
// --force skips all of it, sending no If-Match header at all.

// ifMatchHeaderValue formats revision as the quoted-integer form
// parseRevisionPrecondition requires, e.g. `"7"`.
func ifMatchHeaderValue(revision int64) string {
	return fmt.Sprintf("%q", strconv.FormatInt(revision, 10))
}

// registerIfMatchFlags adds --if-match and --force to fs. Call after fs.Parse
// to read back what the operator gave: revision returns the parsed value and
// whether --if-match was given at all; force reports --force.
func registerIfMatchFlags(fs *flag.FlagSet) (revision func() (int64, bool), force func() bool) {
	var ifMatchRevision int64
	var ifMatchSet bool
	var forceFlag bool
	fs.Func("if-match", "require this exact revision to be current before writing (an integer >= 1); "+
		"omit to have a fresh read supply the current revision automatically; disabled by --force",
		func(s string) error {
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil || v < 1 {
				return fmt.Errorf("must be an integer >= 1, got %q", s)
			}
			ifMatchRevision, ifMatchSet = v, true
			return nil
		})
	fs.BoolVar(&forceFlag, "force", false, "skip the concurrent-write check entirely: send no If-Match header, "+
		"so this write can never be refused because someone else changed the object first")
	return func() (int64, bool) { return ifMatchRevision, ifMatchSet },
		func() bool { return forceFlag }
}

// resolveIfMatch applies the ruled precedence above and returns the exact
// If-Match header value to send ("" for none). fetchCurrentRevision is
// called only when neither an explicit flag nor a payload revision settles
// it; a *cliError it returns with code exitNotFound is read as "this
// object does not exist yet": first creation is not blocked, and this
// still returns ("", nil) rather than propagating the error. Any other
// error is returned to the caller.
func resolveIfMatch(force bool, flagRevision int64, flagSet bool, payloadRevision int64, fetchCurrentRevision func() (int64, error)) (string, error) {
	if force {
		return "", nil
	}
	if flagSet {
		return ifMatchHeaderValue(flagRevision), nil
	}
	if payloadRevision > 0 {
		return ifMatchHeaderValue(payloadRevision), nil
	}
	rev, err := fetchCurrentRevision()
	if err != nil {
		var ce *cliError
		if errors.As(err, &ce) && ce.code == exitNotFound {
			return "", nil
		}
		return "", err
	}
	return ifMatchHeaderValue(rev), nil
}

// wrapperRevision reports the revision an operator's own payload carried,
// when raw is the full object a "<kind> get|show --output json" response
// prints: the same wrapper shape unwrapConfigGetResponse (cmd_config.go)
// recognizes, "kind", "revision" and "payload" all present together at the
// top level. ok is false for a bare payload (no wrapper), a caller-typed
// object that merely happens to have its own "revision" field, or invalid
// JSON: in every one of those cases the caller falls through to a fresh
// GET instead.
func wrapperRevision(raw []byte) (revision int64, ok bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return 0, false
	}
	_, hasPayload := top["payload"]
	_, hasKind := top["kind"]
	revRaw, hasRevision := top["revision"]
	if !hasPayload || !hasKind || !hasRevision {
		return 0, false
	}
	if err := json.Unmarshal(revRaw, &revision); err != nil {
		return 0, false
	}
	return revision, true
}
