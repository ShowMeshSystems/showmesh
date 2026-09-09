package store

import "strings"

// This file formalizes commands.caller_intent's discriminator (owner
// ruling 2026-08-19, renaming commands.requested_revision). The column
// has always held four shapes: a macro run's own "macro:" tag
// (macro_runs.go, present since Step 9), a show.action invocation's plain
// configuration revision, a render dispatch's caller-identity JSON, and a
// cue-catalog deploy's own, differently shaped caller-identity JSON. The
// last two are both bare JSON objects sharing a "node" field, so Go's
// encoding/json (which ignores fields it does not recognize) would decode
// either one into the other's struct without error if a reader ever tried
// both without knowing which family it actually held. Before this file,
// only the macro shape was self-describing; every other shape, including
// telling those two JSON shapes apart from each other, depended entirely
// on cross-referencing the command's own TargetKind and Action, never the
// stored value itself. [FormatCallerIntent] and [ParseCallerIntent] make
// every shape self-describing the same way.

// CallerIntentKind discriminates the shapes commands.caller_intent can
// hold. CallerIntentUntagged is deliberately overloaded: it is both "this
// command names nothing revision-sensitive" (an ordinary command with no
// caller intent to record) and "this row predates schema v26's tagging
// scheme" (a bare revision digit string, a bare JSON identity struct, or
// simply ""). The two are indistinguishable from the stored value alone,
// and this package makes no attempt to tell them apart itself: neither
// [ParseCallerIntent] nor [CallerIntentPayload] ever returns a guessed
// kind for an untagged value.
//
// That does not, by itself, stop a caller from mis-reading anyway: three
// read sites (internal/coordinator/api's render, cue-catalog deploy, and
// action-invoke replay handlers) discard [CallerIntentPayload]'s bool and
// decode whatever string comes back regardless. What actually prevents a
// wrong-family read in practice is each of those sites checking the
// command's own Action or TargetKind FIRST, before ever touching this
// column, so a row of a different family can never reach the decode
// through their idempotency-key lookup. A value TAGGED under this scheme
// (v26 forward) gets a second, incidental layer for free: a lookup under
// the wrong kind returns the string still wearing its own different tag,
// which then fails outright to parse as the shape the caller expected.
// Neither layer covers a pre-v26 untagged JSON value read under the wrong
// family on purpose, which is why the Action/TargetKind check, not this
// column's own content, is this package's real guarantee against the
// defect that let four unrelated shapes, two of them structurally similar
// JSON objects, share one untyped TEXT column in the first place.
type CallerIntentKind string

const (
	// CallerIntentUntagged is never written by [FormatCallerIntent]; it is
	// only ever the result of parsing a value that carries no recognized
	// tag, including the column's own NOT NULL DEFAULT ''.
	CallerIntentUntagged CallerIntentKind = ""

	// CallerIntentRevision tags a plain show.action configuration
	// revision, ARCHITECTURE section 8.1's "requested revision", formatted
	// as its decimal string. Written by internal/coordinator/api's
	// action-invoke endpoint.
	CallerIntentRevision CallerIntentKind = "revision"

	// CallerIntentMacroRun tags a macro-issued command with the run's own
	// pinned (macroObjectID, macroRevision). This kind predates this file:
	// every macro-run row ever written already matches this scheme (see
	// macro_runs.go), so schema v26 needed no backfill for it.
	CallerIntentMacroRun CallerIntentKind = "macro"

	// CallerIntentRenderRequest tags a render-dispatch command with the
	// caller's own unresolved request identity (JSON). See
	// internal/coordinator/api/renderdispatch.go's renderRequestIdentity.
	CallerIntentRenderRequest CallerIntentKind = "render-request"

	// CallerIntentCueCatalogDeploy tags a cue-catalog deploy command with
	// the caller's own unresolved request identity (JSON). See
	// internal/coordinator/api/cuecatalogdeploy.go's
	// cueCatalogDeployRequestIdentity.
	CallerIntentCueCatalogDeploy CallerIntentKind = "cuecatalog-deploy"

	// CallerIntentAssetRemove tags an asset.remove dispatch command
	// with the caller's own unresolved request identity (JSON). See
	// internal/coordinator/api/nodeunusedassets.go's
	// removeNodeAssetRequestIdentity.
	CallerIntentAssetRemove CallerIntentKind = "asset-remove"
)

// callerIntentTagSeparator divides a formatted caller_intent value's kind
// tag from its payload: "<kind><callerIntentTagSeparator><payload>".
const callerIntentTagSeparator = ":"

// callerIntentTags lists every recognized kind [ParseCallerIntent] checks
// for. The tags are prefix-disjoint by construction (none of the kind
// strings above is a prefix of another once callerIntentTagSeparator is
// appended), so scan order does not affect which kind a value matches.
var callerIntentTags = []CallerIntentKind{
	CallerIntentRevision,
	CallerIntentMacroRun,
	CallerIntentRenderRequest,
	CallerIntentCueCatalogDeploy,
	CallerIntentAssetRemove,
}

// FormatCallerIntent tags payload with kind for storage in
// commands.caller_intent, so any reader of the column, not only the call
// site that wrote it, can recover kind from [ParseCallerIntent] without
// consulting the command's own TargetKind or Action.
func FormatCallerIntent(kind CallerIntentKind, payload string) string {
	return string(kind) + callerIntentTagSeparator + payload
}

// ParseCallerIntent splits a stored commands.caller_intent value into the
// kind [FormatCallerIntent] tagged it with. ok is false for "" (nothing
// recorded) and for any value that does not begin with one of this
// package's own recognized tags, including every row written before
// schema v26 introduced this scheme: a bare decimal revision and a bare
// JSON identity struct both come back as (CallerIntentUntagged, s, false).
// A caller getting ok == false back knows only that s is not one of
// today's tagged kinds; it must not infer a kind from s's own shape. See
// [CallerIntentPayload] for a call site that already knows a row's family
// from other evidence and needs to read a pre-v26 value regardless.
func ParseCallerIntent(s string) (kind CallerIntentKind, payload string, ok bool) {
	if s == "" {
		return CallerIntentUntagged, "", false
	}
	for _, k := range callerIntentTags {
		if rest, found := strings.CutPrefix(s, string(k)+callerIntentTagSeparator); found {
			return k, rest, true
		}
	}
	return CallerIntentUntagged, s, false
}

// CallerIntentPayload reads a commands.caller_intent value as kind's own
// payload, for a call site that already knows, from the command's own
// TargetKind or Action rather than from s's shape, that this row can only
// belong to kind's family. Returns (payload, true) for a value
// [FormatCallerIntent] tagged with kind. Returns (s, false) for anything
// else, including a pre-v26 row of this same family (s IS the payload
// there, never wrapped in a tag to begin with) and a genuinely empty "".
func CallerIntentPayload(kind CallerIntentKind, s string) (payload string, tagged bool) {
	if k, p, ok := ParseCallerIntent(s); ok && k == kind {
		return p, true
	}
	return s, false
}
