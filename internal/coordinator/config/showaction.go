package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file and showmacro.go are Step 9 wave 2 Builder A's own addition
// (STEP-9-SPEC.md section 5, ADR-027 decision 2): the config_objects.kind /
// config_revisions.payload_json shape for the two new configuration kinds,
// "show.action" and "show.macro". STEP-9-SPEC.md section 3 is explicit that
// there is no generic kind registry and that fpp.endpoints (fppendpoints.go)
// is hand-written end to end; this file follows the identical pattern
// rather than inventing an abstraction two kinds does not justify.
//
// Unlike fpp.endpoints, both kinds here need write-time validation that
// depends on state this package does not own (the configured FPP endpoint
// list, the declared MQTT integration brokers, the Step 8 FPP primitive
// registry, and — for show.macro — the set of existing show.action object
// ids). Every one of those is taken as a parameter to the Decode functions
// below rather than fetched, per the wave 2 shared contract section 3 and
// STEP-9-SPEC.md sections 5.3/5.4/5.6: this package has no store access and
// must not import internal/coordinator/api (that import direction is
// forced the other way — see FPPPrimitiveRegistry's own doc comment).

const (
	// ShowActionConfigKind is config_objects.kind and config_revisions.kind
	// for a show.action object, and the second path segment of
	// GET/PUT /api/v1/config/show.action/{id} (Builder C's route, not built
	// here). Unlike FPPEndpointsConfigObjectID, show.action is a
	// collection: each object id is the action's own identifier, chosen by
	// the caller, not a fixed singleton.
	ShowActionConfigKind = "show.action"

	// ShowMacroConfigKind is show.macro's own equivalent of
	// ShowActionConfigKind. See showmacro.go.
	ShowMacroConfigKind = "show.macro"
)

// --- The api-side dependency this package needs but must not import. ---

// FPPPrimitiveRegistry is the eight registered Step 8 primitives as this
// package needs them when validating an authored show.action, declared here
// (rather than in internal/coordinator/api) because the import direction
// between config and api is forced: internal/coordinator/macro imports
// internal/coordinator/api (wave 2 shared contract section 1), so api must
// never import macro, and api/showaction_registry.go implements this
// interface over its own unexported registry rather than this package
// importing api and creating the cycle.
type FPPPrimitiveRegistry interface {
	// DecodeActionParams validates and normalizes an authored action's
	// params against the named primitive, applying the same absent /
	// explicit-null / unknown-key rules the HTTP command endpoint applies
	// (internal/coordinator/api's decodeFPPCommandParams, plus that
	// primitive's own ValidateParams — STEP-9-SPEC.md section 5.3: "an
	// action authored with a bad playlist type fails at write time rather
	// than at 17:00"). raw is the decoded "target" object of a show.action
	// payload (a map keyed by JSON field name, e.g. "instanceId",
	// "primitive", "params"), mirroring decodeFPPCommandParams's own "top"
	// parameter shape exactly so the adapter can pass it through unchanged
	// rather than re-wrapping it.
	DecodeActionParams(wireAction string, raw map[string]json.RawMessage) (map[string]any, error)

	// Decision11Class is the primitive's own registered safety class, in
	// the show.action safetyClass vocabulary ("none" | "blackout" |
	// "stop" | "powerOff"). ok is false for an unregistered action.
	Decision11Class(wireAction string) (class string, ok bool)

	// WireActions is the registered vocabulary, for an error naming what
	// is supported.
	WireActions() []string
}

// --- Validation errors: machine-readable, per the wave 2 shared contract
// section 4 ("a client that must tell two refusals apart may never branch
// on prose"). ---

// ValidationError is the typed error every Decode function in this file and
// showmacro.go returns on a rejected write. Code is a small, closed,
// exported set (below) so Builder C can map it onto a problem type URI
// without reading Detail's prose; Field names where in the payload the
// problem is, using a dotted/indexed path ("target.publish.qos",
// "steps[2].action"); Detail is the operator-facing sentence — and, per the
// wave 2 shared contract section 4 and CLAUDE.md's own standing rule, it
// carries no repo path, no .md reference, no ADR number, and no section
// citation. The reasoning for why a rule exists lives in this file's Go
// doc comments, never in Detail.
type ValidationError struct {
	Code   string
	Field  string
	Detail string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Detail)
}

// The closed set of Codes this file and showmacro.go produce.
//
// Most structural problems (a field absent, present as an explicit JSON
// null, present as an empty string where that is not a valid choice, or of
// the wrong JSON type / not a member of a closed enum) share one of the
// four generic codes below and are told apart by Field, which always names
// the exact dotted path. A handful of business rules get their OWN code
// rather than folding into ValidationCodeFieldInvalid, because the wave 2
// shared contract and STEP-9-SPEC.md name them explicitly as needing to be
// distinguishable from an ordinary bad value:
//
//   - ValidationCodeSafetyClassMismatch: an FPP action's declared
//     safetyClass disagrees with its primitive's own registered class
//     (STEP-9-SPEC.md section 5.3 — "This is a distinct Code, not folded
//     into a generic bad-value error").
//   - ValidationCodeLocalFallbackReduced: a macro step declared
//     localFallback.class "reduced" (STEP-9-SPEC.md section 5.4 —
//     "reduced is not an accepted value ... Its own Code").
//   - ValidationCodeStepsEmpty / ValidationCodeStepsTooMany /
//     ValidationCodeStepIDDuplicate: showmacro.go's own structural rules
//     that are not "one field, one problem" (they are about the shape of
//     the whole steps array), so a shared field-level code would not let a
//     client distinguish "too many steps" from "a bad value inside one
//     step" without also parsing Field.
const (
	// ValidationCodeBodyInvalid means the payload was not a JSON object at
	// all (Field is empty).
	ValidationCodeBodyInvalid = "body-invalid"

	// ValidationCodeFieldRequired means Field was absent.
	ValidationCodeFieldRequired = "field-required"

	// ValidationCodeFieldNull means Field was present as an explicit JSON
	// null — always an error in this step's payloads: absent, null, and
	// explicitly empty are three different things (CLAUDE.md, restated for
	// every write surface this step adds), and no field in show.action or
	// show.macro treats a present null as "use the default".
	ValidationCodeFieldNull = "field-null"

	// ValidationCodeFieldEmpty means Field was present as an empty string
	// where an empty string is not a valid choice for that field (a
	// required label, id, or action reference).
	ValidationCodeFieldEmpty = "field-empty"

	// ValidationCodeFieldInvalid means Field was present and non-null but
	// failed some other check: wrong JSON type, not a member of a closed
	// enum, out of range, or a nested object's own shape rule.
	ValidationCodeFieldInvalid = "field-invalid"

	// ValidationCodeFieldUnknownReference means Field named something that
	// must resolve against caller-supplied state and did not: an
	// unconfigured FPP instanceId, an undeclared MQTT broker identifier, or
	// (show.macro) a step.action that is not an existing show.action
	// object id (STEP-9-SPEC.md section 5.6).
	ValidationCodeFieldUnknownReference = "field-unknown-reference"

	// ValidationCodeSafetyClassMismatch: see this const block's own doc
	// comment above.
	ValidationCodeSafetyClassMismatch = "safety-class-mismatch"

	// ValidationCodeLocalFallbackReduced: see this const block's own doc
	// comment above. Defined here rather than in showmacro.go because every
	// other Code in the closed set lives in this one place.
	ValidationCodeLocalFallbackReduced = "local-fallback-reduced"

	// ValidationCodeStepsEmpty, ValidationCodeStepsTooMany,
	// ValidationCodeStepIDDuplicate: see this const block's own doc comment
	// above. showmacro.go is the only user.
	ValidationCodeStepsEmpty      = "steps-empty"
	ValidationCodeStepsTooMany    = "steps-too-many"
	ValidationCodeStepIDDuplicate = "step-id-duplicate"

	// ValidationCodeFieldUnknownKey means the payload's top-level object
	// carried a key this kind does not recognize. Added by review: before
	// this code existed, an unrecognized top-level key (most dangerously a
	// typo of a real one, e.g. "onFailur") was silently ignored by every
	// decode function in this file and showmacro.go, which for a field
	// with a default (description, onFailure, onUnconfirmed,
	// target.publish.retain) meant the coordinator silently stored a
	// DIFFERENT policy than the one the operator typed, with no error at
	// all — the worst of the three possible outcomes (reject, silently
	// ignore, silently apply a default), because the operator has no
	// signal anything went wrong. decodeFPPCommandParams
	// (internal/coordinator/api/fppcommand_primitives.go) already refused
	// an unrecognized params key for exactly this reason; this code
	// brings show.action and show.macro's own top-level object to the
	// same posture, matching what api/openapi.yaml's conformance-test
	// overlay already treats these payloads as (closed objects).
	ValidationCodeFieldUnknownKey = "field-unknown-key"

	// The Codes below are Track F seam F1's own additions (nightsession.go),
	// for night.session/night.session.active rules that are not "one
	// field, one problem" any more than ValidationCodeStepsEmpty/
	// StepIDDuplicate were for show.macro. Defined here rather than in
	// nightsession.go for the same reason ValidationCodeLocalFallbackReduced
	// is defined here rather than in showmacro.go: every Code in this
	// closed set lives in one place. (Deliberately no exact count in this
	// comment — review found the count already wrong once, at "five" for
	// six Codes; a number here is a claim this comment cannot keep honest
	// on its own the next time one is added.)

	// ValidationCodeCalendarFieldRejected means the payload carried a key
	// named "at", "cron", "schedule", "time", "date", "weekday", or
	// "timezone" anywhere in the object tree — ADR-038 decision 1: FPP is
	// the authoritative calendar scheduler, so no ShowMesh object may
	// carry a wall-clock or dated value, and this is a validation rule
	// rather than a naming convention an author could still work around.
	ValidationCodeCalendarFieldRejected = "calendar-field-rejected"

	// ValidationCodeDuplicateRestDuration means the payload restated the
	// resting FSEQ's own length under a key like "restDuration" or
	// "restSeconds", anywhere in the object tree. RESTING-MODE.md §6.1:
	// the FSEQ is the only duration authority, so a second, hand-entered
	// number for the same fact is a value that can silently disagree with
	// it the day someone changes the FSEQ and forgets the field.
	ValidationCodeDuplicateRestDuration = "duplicate-rest-duration"

	// ValidationCodeNotImplemented means the payload named something the
	// records specify but no code enforces yet. Track F seam F1 produced it
	// for night.session's "siteControl" and "interlocks"; seam F6
	// (nightsitecontrol.go) now decodes and validates both, so that reason
	// is gone. show.playlist's reserved "showmesh" runner still returns it
	// (ADR-043, no implementation), so the Code is live, not retired.
	// Accepting configuration nothing enforces is a surface asserting
	// something false (owner decision, TRACK-F-F1 seam spec).
	ValidationCodeNotImplemented = "not-implemented"

	// The Codes below are Track F seam F6's own additions
	// (nightsitecontrol.go, RESTING-MODE.md §10, ADR-016, ADR-029):
	// interlocks and siteControl rules that are not "one field, one
	// problem," the same reasoning ValidationCodeCueNameDuplicate and
	// ValidationCodeCrossShowReference above already carry for F1's own
	// structural rules.

	// ValidationCodeInterlockNameDuplicate means two entries of
	// night.session.interlocks declared the same name: RESTING-MODE.md
	// §10.1: "Rule names are unique within a configuration revision."
	ValidationCodeInterlockNameDuplicate = "interlock-name-duplicate"

	// ValidationCodeInterlockSignalNotConfirmable means an interlock's
	// signal named a show.action that cannot produce the evidence read an
	// interlock needs: either its target is not mqtt, or its
	// target.expect.kind is "none". An interlock is a request/response
	// evidence read (orchestrator ruling, this seam's own build brief);
	// an action nothing ever answers can never resolve one.
	ValidationCodeInterlockSignalNotConfirmable = "interlock-signal-not-confirmable"

	// ValidationCodePowerDomainRefused means a power binding declared a
	// powerDomain this position in the schema does not accept:
	// presentationPowerOn/presentationPowerOff both require
	// "presentation" (RESTING-MODE.md §10.2: "power-down-presentation
	// accepts only bindings declared as powerDomain: presentation. It
	// rejects environmental, mixed, and unknown bindings rather than
	// guessing," applied at write time to the binding the command actually
	// invokes rather than deferred to the moment the command runs).
	ValidationCodePowerDomainRefused = "power-domain-refused"

	// ValidationCodeDomainProvenanceRefused means a power binding declared
	// domainProvenance "provider": refused because no control provider in
	// this build can authoritatively identify what is physically behind an
	// mqtt/Home Assistant binding (RESTING-MODE.md §10.2, ADR-016): every
	// binding this build can express is operator-declared, and trusting a
	// "provider" claim here would be exactly the guess this field exists
	// to prevent.
	ValidationCodeDomainProvenanceRefused = "domain-provenance-refused"

	// ValidationCodePrerequisitesEmpty means presentationPowerOff selected
	// removalPolicy "after-actions" but supplied zero prerequisites;
	// RESTING-MODE.md §10.2: "a non-empty ordered list."
	ValidationCodePrerequisitesEmpty = "prerequisites-empty"

	// ValidationCodePowerOffPrerequisiteCycle means an "after-actions"
	// prerequisite named the SAME presentation power-off binding's own
	// action: RESTING-MODE.md §10.2 says prerequisites "may not invoke the
	// same power-off binding directly or indirectly." This build has
	// exactly one presentation power-off binding per session and no
	// action-to-action call graph, so the only reachable cycle is direct
	// self-reference; see nightsitecontrol.go's own doc comment on
	// decodeNightPrerequisite for why indirect reference is not a
	// distinct, separately-detected case here.
	ValidationCodePowerOffPrerequisiteCycle = "power-off-prerequisite-cycle"

	// ValidationCodeInterlockShutdownPhaseRequiresOverride means a "block"
	// rule declared on phase fade-out-night or power-down-presentation set
	// overridePolicy "none": those two phases end the night, and a guard
	// on either one must always have a human exit (RESTING-MODE.md
	// §10.4, orchestrator ruling from this seam's own safety review).
	ValidationCodeInterlockShutdownPhaseRequiresOverride = "interlock-shutdown-phase-requires-override"

	// ValidationCodeInterlockSignalNoFalseAnswer means a "block" rule's
	// signal action uses an mqtt expect kind that can never report a
	// negative answer ("text", or "number" with no comparison value), so
	// the rule could only ever withhold via onUnavailable, never via a
	// real condition-false reading.
	ValidationCodeInterlockSignalNoFalseAnswer = "interlock-signal-no-false-answer"

	// ValidationCodeBackgroundAudioItemsEmpty means
	// resting.backgroundAudio.items was present but held zero entries —
	// AUDIO-ENGINE.md §3 requires at least one item for a playlist to
	// mean anything.
	ValidationCodeBackgroundAudioItemsEmpty = "background-audio-items-empty"

	// ValidationCodeItemIDDuplicate means two entries of
	// resting.backgroundAudio.items declared the same itemId. Mirrors
	// ValidationCodeStepIDDuplicate: pkg/audio.PlaylistItem identifies an
	// item by ItemID, not position, so a duplicate id is ambiguous the
	// moment the list is reordered.
	ValidationCodeItemIDDuplicate = "item-id-duplicate"

	// ValidationCodeCueNameDuplicate means two cues in the same
	// enterShow.cues or enterResting.cues list declared the same name.
	ValidationCodeCueNameDuplicate = "cue-name-duplicate"

	// ValidationCodeCrossShowReference means a cue's action, the resting
	// timeline asset, or a backgroundAudio item named an object belonging
	// to a DIFFERENT show than this night.session's own "show" field.
	// ADR-027 makes Show a namespace precisely so that programming
	// Christmas cannot accidentally break Halloween (owner ruling,
	// 2026-08-18 review): every cross-object reference this payload
	// carries is checked against the session's own show, not merely
	// checked for existence.
	ValidationCodeCrossShowReference = "cross-show-reference"

	// ValidationCodeBackgroundAudioMixedTargets means
	// resting.backgroundAudio.items named more than one distinct asset
	// target: a background-audio playlist plays on exactly one audio
	// output, and each item's own "target" (ADR-028 asset identity) is
	// this seam's only source of which audio.node id that is, so every
	// item must agree.
	ValidationCodeBackgroundAudioMixedTargets = "background-audio-mixed-targets"

	// ValidationCodeAnnouncementPolicyNotApplicable means a cue named
	// announcementPolicy while its own role was not "announcement": the
	// duck/mix/interrupt policy only ever applies to an announcement.
	ValidationCodeAnnouncementPolicyNotApplicable = "announcement-policy-not-applicable"
	// ValidationCodeAudioNodeChannelDuplicate: audionode.go's own rule.
	// programChannels lists the SAME physical route's channel indices, so a
	// repeated index is a distinct refusal from an ordinary out-of-range
	// value.
	ValidationCodeAudioNodeChannelDuplicate = "audio-node-channel-duplicate"

	// ValidationCodeAudioNodeChannelOverlap: audionode.go's own rule.
	// ltcChannel named an index already claimed by programChannels — ADR-018
	// requires LTC on a channel discrete from program, so this is a
	// placement conflict, not merely a bad number.
	ValidationCodeAudioNodeChannelOverlap = "audio-node-channel-overlap"

	// ValidationCodeAudioNodeRouteMismatch: audionode.go's own rule.
	// programRoute and ltcRoute named different routes — program and LTC
	// leave through one interface in one clock domain (ADR-018), so two
	// different route names can never be satisfied together.
	ValidationCodeAudioNodeRouteMismatch = "audio-node-route-mismatch"

	// Track H seam H1's own additions (showplaylist.go).

	// ValidationCodeEntriesEmpty means show.playlist.entries was present
	// but held zero entries — TRACK-H-H1-SPEC.md section 4: "an empty
	// entries list" is refused, matching ValidationCodeStepsEmpty's and
	// ValidationCodeBackgroundAudioItemsEmpty's own per-kind precedent
	// rather than reusing either directly.
	ValidationCodeEntriesEmpty = "entries-empty"

	// ValidationCodeEntryPositionDuplicate means two show.playlist entries
	// declared the same (fpp.section, fpp.position) pair. TRACK-H-H1-SPEC.md
	// section 3.1: two entries at the same section and position derive the
	// same entry key, and no runtime evidence could ever tell them apart —
	// distinct from ValidationCodeItemIDDuplicate, which this file reuses
	// for a duplicate entry id.
	ValidationCodeEntryPositionDuplicate = "entry-position-duplicate"
)

// --- show.action's own vocabulary. ---

// The four members of show.action.safetyClass, matching ADR-024 decision
// 11's own named list exactly and adding no members (STEP-9-SPEC.md section
// 5.3, ADR-031 decision 5).
const (
	ShowSafetyClassNone     = "none"
	ShowSafetyClassBlackout = "blackout"
	ShowSafetyClassStop     = "stop"
	ShowSafetyClassPowerOff = "powerOff"
)

var showSafetyClasses = map[string]bool{
	ShowSafetyClassNone:     true,
	ShowSafetyClassBlackout: true,
	ShowSafetyClassStop:     true,
	ShowSafetyClassPowerOff: true,
}

// The four members of show.action.target.integration. "audio" reaches
// pkg/audio's session command surface through an ordinary logical action
// binding (ADR-029), the same as fpp/mqtt/resolume — see
// docs/build/IDENTIFIER-REGISTER.md's "show.action target integrations"
// table.
const (
	ShowActionIntegrationFPP      = "fpp"
	ShowActionIntegrationMQTT     = "mqtt"
	ShowActionIntegrationResolume = "resolume"
	ShowActionIntegrationAudio    = "audio"
)

// showActionAudioActions is every audio.session.*/audio.gain.*/
// audio.output.* operation name this coordinator's agent-facing dispatch
// already ships (internal/coordinator/api/audiodispatch.go's own
// handleAudioSession*/handleAudioGain*/handleAudioOutput* route set) — an
// audio show.action target names one of these, never a new operation
// name.
var showActionAudioActions = []string{
	"audio.session.apply", "audio.session.prepare", "audio.session.start",
	"audio.session.pause", "audio.session.resume", "audio.session.seek",
	"audio.session.advance", "audio.session.stop", "audio.session.clear",
	"audio.gain.set", "audio.gain.fade",
	"audio.output.mute", "audio.output.unmute",
}

var showActionAudioActionSet = func() map[string]bool {
	m := make(map[string]bool, len(showActionAudioActions))
	for _, a := range showActionAudioActions {
		m[a] = true
	}
	return m
}()

// The seven Resolume action names (internal/coordinator/collector/resolume.ActionName),
// duplicated here by value rather than by import: this package must not
// import that package or internal/coordinator/api.
const (
	ShowActionResolumeLaunchClip     = "launchClip"
	ShowActionResolumeClearLayer     = "clearLayer"
	ShowActionResolumeBlackout       = "blackout"
	ShowActionResolumeLaunchColumn   = "launchColumn"
	ShowActionResolumeSelectDeck     = "selectDeck"
	ShowActionResolumeSetLayerBypass = "setLayerBypass"
	ShowActionResolumeSetLayerMaster = "setLayerMaster"
)

// showActionResolumeActionNames is every accepted resolume action, sorted,
// for an error naming what is supported (mirroring registry.WireActions'
// identical role for the fpp branch).
var showActionResolumeActionNames = []string{
	ShowActionResolumeBlackout,
	ShowActionResolumeClearLayer,
	ShowActionResolumeLaunchClip,
	ShowActionResolumeLaunchColumn,
	ShowActionResolumeSelectDeck,
	ShowActionResolumeSetLayerBypass,
	ShowActionResolumeSetLayerMaster,
}

var showActionResolumeActions = map[string]bool{
	ShowActionResolumeLaunchClip:     true,
	ShowActionResolumeClearLayer:     true,
	ShowActionResolumeBlackout:       true,
	ShowActionResolumeLaunchColumn:   true,
	ShowActionResolumeSelectDeck:     true,
	ShowActionResolumeSetLayerBypass: true,
	ShowActionResolumeSetLayerMaster: true,
}

// resolumeActionDeclaredSafetyClass mirrors
// internal/coordinator/collector/resolume's own safety-class
// classification. clearLayer is blackout, not none: both make the wall
// darker, and ShowMesh never refuses that.
// TestResolumeActionSafetyClassMatchesResolumeRegistry reconciles this
// table against that package's own registry so the two cannot diverge.
var resolumeActionDeclaredSafetyClass = map[string]string{
	ShowActionResolumeBlackout:       ShowSafetyClassBlackout,
	ShowActionResolumeClearLayer:     ShowSafetyClassBlackout,
	ShowActionResolumeLaunchClip:     ShowSafetyClassNone,
	ShowActionResolumeLaunchColumn:   ShowSafetyClassNone,
	ShowActionResolumeSelectDeck:     ShowSafetyClassNone,
	ShowActionResolumeSetLayerBypass: ShowSafetyClassNone,
	ShowActionResolumeSetLayerMaster: ShowSafetyClassNone,
}

// ResolumeActionDeclaredSafetyClass exposes resolumeActionDeclaredSafetyClass
// to a test that reconciles it against
// internal/coordinator/collector/resolume's own registry (that test lives
// in an external config_test package, which CAN import both without a
// cycle — this package itself never may).
func ResolumeActionDeclaredSafetyClass(action string) (string, bool) {
	c, ok := resolumeActionDeclaredSafetyClass[action]
	return c, ok
}

// ResolumeActionNames returns every resolume action this package declares
// a safety class for — the keys of resolumeActionDeclaredSafetyClass
// itself, not the separate showActionResolumeActionNames vocabulary list,
// so a reconciliation test comparing this against resolume's own registry
// is actually comparing the map under test, not a second list that could
// drift from it unnoticed.
func ResolumeActionNames() []string {
	out := make([]string, 0, len(resolumeActionDeclaredSafetyClass))
	for name := range resolumeActionDeclaredSafetyClass {
		out = append(out, name)
	}
	return out
}

// resolumeRefKind is one target.ref value's required wire JSON type.
type resolumeRefKind int

const (
	resolumeRefString resolumeRefKind = iota
	resolumeRefBool
	resolumeRefNumber
)

// resolumeRefParam describes one named key target.ref may carry for one
// action.
type resolumeRefParam struct {
	Name     string
	Kind     resolumeRefKind
	Required bool
}

// resolumeActionRefVocabulary is each action's own named-reference
// vocabulary, duplicated by value from
// internal/coordinator/resolumeactionwiring.go's identical
// resolumeActionParamVocabulary. "deck" is launchClip's own conditional
// key (required unless "persistent" is true): declared optional here and
// enforced by validateResolumeRefConditionals.
var resolumeActionRefVocabulary = map[string][]resolumeRefParam{
	ShowActionResolumeLaunchClip: {
		{Name: "clip", Kind: resolumeRefString, Required: true},
		{Name: "deck", Kind: resolumeRefString, Required: false},
		{Name: "layer", Kind: resolumeRefString, Required: false},
		{Name: "persistent", Kind: resolumeRefBool, Required: false},
	},
	ShowActionResolumeClearLayer: {
		{Name: "layer", Kind: resolumeRefString, Required: true},
	},
	ShowActionResolumeBlackout: {},
	ShowActionResolumeLaunchColumn: {
		{Name: "column", Kind: resolumeRefString, Required: true},
		{Name: "deck", Kind: resolumeRefString, Required: true},
	},
	ShowActionResolumeSelectDeck: {
		{Name: "deck", Kind: resolumeRefString, Required: true},
	},
	ShowActionResolumeSetLayerBypass: {
		{Name: "layer", Kind: resolumeRefString, Required: true},
		{Name: "bypassed", Kind: resolumeRefBool, Required: true},
	},
	ShowActionResolumeSetLayerMaster: {
		{Name: "layer", Kind: resolumeRefString, Required: true},
		{Name: "master", Kind: resolumeRefNumber, Required: true},
	},
}

// ErrResolumeCompositionNotUploaded is what a [ResolumeReferenceResolver]
// method returns when no composition has ever been uploaded. Declared here
// rather than imported from internal/coordinator/collector/resolume, so
// this package's own error vocabulary does not depend on that package's;
// an implementation translates that package's own ErrCompositionNotUploaded
// to this sentinel at the boundary.
var ErrResolumeCompositionNotUploaded = errors.New("resolume: no composition has been uploaded to this coordinator yet")

// ResolumeClipReference is launchClip's own reference vocabulary, mirroring
// internal/coordinator/collector/resolume's ClipReference field for field
// so an implementation of [ResolumeReferenceResolver] can pass it straight
// through without a second translation.
type ResolumeClipReference struct {
	Clip       string
	Deck       string
	Persistent bool
	Layer      string
}

// ResolumeReferenceResolver resolves a named reference against this
// coordinator's currently stored composition, following the same pattern
// [FPPPrimitiveRegistry] already established: declared here, at the
// consumer, implemented over internal/coordinator/collector/resolume
// somewhere that may import both, never in this package. It exposes no
// Resolume object id to this package (ADR-037): every method returns only
// an error, never an id.
//
// nil means resolved; [ErrResolumeCompositionNotUploaded] means nothing
// has ever been uploaded; any other error's Error() text already names
// the label (not found) or every candidate (ambiguous), and an
// implementation returns it unchanged.
type ResolumeReferenceResolver interface {
	ResolveClip(ref ResolumeClipReference) error
	ResolveLayer(name string) error
	ResolveColumn(deck, column string) error
	ResolveDeck(name string) error
}

// The five members of show.action.target.expect.kind (STEP-9-SPEC.md
// section 7.3).
const (
	MQTTExpectKindNone    = "none"
	MQTTExpectKindBoolean = "boolean"
	MQTTExpectKindNumber  = "number"
	MQTTExpectKindText    = "text"
	MQTTExpectKindMatch   = "match"
)

var mqttExpectKinds = map[string]bool{
	MQTTExpectKindNone:    true,
	MQTTExpectKindBoolean: true,
	MQTTExpectKindNumber:  true,
	MQTTExpectKindText:    true,
	MQTTExpectKindMatch:   true,
}

// mqttExpectMaxDeadlineSeconds is STEP-9-SPEC.md section 7.3's 120-second
// cap on target.expect.deadlineSeconds, and internal/coordinator/broker's
// MaxResponseDeadline (broker/response.go) is the SAME 120 seconds by
// design — the wave 2 shared contract section 3 asks this package to
// import that constant rather than duplicate the literal, "unless that
// import is unacceptable from config, in which case say so in your report
// and put a test that asserts the two numbers agree."
//
// That import is unacceptable: internal/coordinator/broker already imports
// internal/coordinator/config (broker.go's own import block, for
// config.Config), so config importing broker back would be an import
// cycle. The literal is therefore repeated here, and
// TestMQTTExpectMaxDeadlineSecondsAgreesWithBrokerMaxResponseDeadline (an
// external config_test package test, which CAN import both without a
// cycle) asserts the two stay equal.
const mqttExpectMaxDeadlineSeconds = 120

// --- show.action payload shape. ---

// showActionTopLevelKeys is the complete set of keys
// DecodeShowActionPayload recognizes at the top level of the request
// body — see rejectUnknownTopLevelKeys.
var showActionTopLevelKeys = map[string]bool{
	"show": true, "label": true, "description": true, "safetyClass": true, "target": true,
}

// ShowActionPayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowActionConfigKind]. Every value here has already passed
// DecodeShowActionPayload's rules; nothing downstream needs to re-check
// absent/null/empty, an unresolved reference, or a safety-class mismatch.
type ShowActionPayload struct {
	Show        string           `json:"show"`
	Label       string           `json:"label"`
	Description string           `json:"description,omitempty"`
	SafetyClass string           `json:"safetyClass"`
	Target      ShowActionTarget `json:"target"`
}

// ShowActionTarget is show.action.target, flattened exactly as STEP-9-
// SPEC.md section 5.3's wire examples show it (integration plus either the
// fpp fields or the mqtt fields directly, never nested a second level under
// an "fpp"/"mqtt" key) — this is the shape Builder C's wire types and
// api/openapi.yaml are expected to mirror.
type ShowActionTarget struct {
	Integration string `json:"integration"`

	// fpp-only. Empty/nil when Integration is "mqtt".
	InstanceID string `json:"instanceId,omitempty"`
	Primitive  string `json:"primitive,omitempty"`

	// Params is the integration's own parameter map: an fpp primitive's
	// decoded/validated params (registry.DecodeActionParams), or an
	// audio target's own command params, passed through undecoded — this
	// package has no audio operation parameter registry (that vocabulary
	// lives in internal/agent, across a layer this package must not
	// import); the coordinator's audio dispatch path validates them at
	// dispatch time the same way it already does for a direct
	// audio.session.* API call.
	Params map[string]any `json:"params,omitempty"`

	// mqtt-only. Empty/nil when Integration is "fpp".
	Broker  string                 `json:"broker,omitempty"`
	Publish *ShowActionMQTTPublish `json:"publish,omitempty"`
	Expect  *ShowActionMQTTExpect  `json:"expect,omitempty"`

	// resolume-only. Empty/nil when Integration is "fpp" or "mqtt". Action
	// is one of the seven ShowActionResolume* names; Ref carries seam B's
	// named-reference vocabulary verbatim (clip, deck, layer, column,
	// persistent, bypassed, master) — never an object id (ADR-037).
	Action string         `json:"action,omitempty"`
	Ref    map[string]any `json:"ref,omitempty"`

	// audio-only. Empty/nil unless Integration is "audio". AudioAction is
	// one of [showActionAudioActions]; Params (above) carries that
	// operation's own command params, exactly as a direct
	// audio.session.*/audio.gain.*/audio.output.* API call would.
	AudioNodeID    string `json:"audioNodeId,omitempty"`
	AudioSessionID string `json:"audioSessionId,omitempty"`
	AudioAction    string `json:"audioAction,omitempty"`
}

// ShowActionMQTTPublish is show.action.target.publish.
type ShowActionMQTTPublish struct {
	Topic string `json:"topic"`
	// Payload is a real MQTT payload and an explicit empty string is a
	// valid, meaningful value (an empty publish is ordinary MQTT usage) —
	// only absent and explicit null are rejected. See
	// decodeRequiredStringAllowEmpty.
	Payload string `json:"payload"`
	QoS     int    `json:"qos"`
	// Retain defaults to false when absent; a present null is an error.
	// This is a deliberate THIRD absent-carries-meaning field beyond
	// onFailure/onUnconfirmed — see this package's showmacro.go doc comment
	// on ShowMacroOnFailureDefault for why the wave 2 shared contract's "the
	// only two keys" line is read as scoped to show.macro's policy axes,
	// not as a blanket rule over every payload this step adds, and why this
	// builder followed the wave2-builder-a.md brief's explicit,
	// field-specific instruction ("retain ... defaults false") over that
	// more general line rather than silently picking one. Flagged in this
	// builder's own report as a place the two source documents disagreed.
	Retain bool `json:"retain"`
}

// ShowActionMQTTExpect is show.action.target.expect. Value is a pointer so
// "value absent" and "value present" are distinguishable; which kinds
// accept, require, or forbid it is decodeMQTTExpect's own rule (a judgment
// call this builder made beyond what STEP-9-SPEC.md section 7.3 states
// explicitly for "boolean" and "text" — see this builder's report).
//
// Value is always a Go string, deliberately, for BOTH kinds that use it.
// "match" compares it byte-for-byte against the response payload, so a
// string is the only sensible shape. "number" is a JSON-round-trip defect
// found and fixed after this file first shipped: this field is the exact
// text the operator typed (e.g. "42" or "4.2e1"), parsed with
// strconv.ParseFloat only to confirm it is a valid number, never
// normalized to a float and re-formatted — a JSON number decoded through
// Go's float64 does not survive being written back out unchanged (the
// wave 2 command-confirmation defect where an int64 came back as a
// float64 is the same family of bug), and this field is read back on
// every GET and PUT of the same object, so its wire representation must
// be stable under that round trip. Storing and reading it as a string
// keeps that promise; decodeMQTTExpect accepts it as a JSON string on the
// way in for the identical reason — see that function's own comment.
type ShowActionMQTTExpect struct {
	Kind  string  `json:"kind"`
	Topic string  `json:"topic,omitempty"`
	Value *string `json:"value,omitempty"`
	// DeadlineSeconds is 0 (omitted) only for kind "none".
	DeadlineSeconds int `json:"deadlineSeconds,omitempty"`
}

// EncodeShowActionPayload marshals p into config_revisions.payload_json's
// column shape, mirroring [EncodeFPPEndpointsPayload]'s own pattern. p is
// assumed already valid (the product of DecodeShowActionPayload); this
// function does not re-validate.
func EncodeShowActionPayload(p ShowActionPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.action payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowActionPayload parses and validates raw against STEP-9-SPEC.md
// section 5.3's rules, and the resolume branch's own rules. endpoints is
// the caller's currently-configured FPP endpoint list (Config.FPPEndpoints
// or the store-authoritative equivalent); brokers is the caller's declared
// integration broker set (see integrationbrokers.go); registry resolves
// and validates an FPP primitive's own parameter vocabulary and safety
// class; resolver resolves a resolume target's seam B reference against
// the currently stored composition; showExists reports whether "show"
// names an existing show config object. None of the five is fetched by
// this package — see this file's own top doc comment.
func DecodeShowActionPayload(raw string, endpoints []FPPEndpoint, brokers []IntegrationBroker, registry FPPPrimitiveRegistry, resolver ResolumeReferenceResolver, showExists func(string) bool) (ShowActionPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowActionPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showActionTopLevelKeys); verr != nil {
		return ShowActionPayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowActionPayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowActionPayload{}, verr
	}
	if !showExists(show) {
		return ShowActionPayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show; create it first", show),
		}
	}

	label, verr := decodeRequiredString(top, "label", "label")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	description, verr := decodeOptionalString(top, "description", "description")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	safetyClass, verr := decodeRequiredString(top, "safetyClass", "safetyClass")
	if verr != nil {
		return ShowActionPayload{}, verr
	}
	if !showSafetyClasses[safetyClass] {
		return ShowActionPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "safetyClass",
			Detail: "safetyClass must be one of none, blackout, stop, or powerOff",
		}
	}

	targetFields, verr := decodeRequiredObject(top, "target", "target")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	integration, verr := decodeRequiredString(targetFields, "integration", "target.integration")
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	var target ShowActionTarget
	switch integration {
	case ShowActionIntegrationFPP:
		target, verr = decodeFPPTarget(targetFields, safetyClass, endpoints, registry)
	case ShowActionIntegrationMQTT:
		target, verr = decodeMQTTTarget(targetFields, brokers)
	case ShowActionIntegrationResolume:
		target, verr = decodeResolumeTarget(targetFields, safetyClass, resolver)
	case ShowActionIntegrationAudio:
		target, verr = decodeAudioTarget(targetFields, safetyClass)
	default:
		verr = &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.integration",
			Detail: "integration must be \"fpp\", \"mqtt\", \"resolume\", or \"audio\"",
		}
	}
	if verr != nil {
		return ShowActionPayload{}, verr
	}

	return ShowActionPayload{
		Show:        show,
		Label:       label,
		Description: description,
		SafetyClass: safetyClass,
		Target:      target,
	}, nil
}

// decodeFPPTarget decodes and validates target.integration == "fpp".
// declaredSafetyClass is the payload's own top-level safetyClass, needed
// here to enforce STEP-9-SPEC.md section 5.3's agreement rule.
func decodeFPPTarget(targetFields map[string]json.RawMessage, declaredSafetyClass string, endpoints []FPPEndpoint, registry FPPPrimitiveRegistry) (ShowActionTarget, *ValidationError) {
	instanceID, verr := decodeRequiredString(targetFields, "instanceId", "target.instanceId")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !fppInstanceConfigured(instanceID, endpoints) {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.instanceId",
			Detail: fmt.Sprintf("instance %q is not a configured FPP endpoint", instanceID),
		}
	}

	primitive, verr := decodeRequiredString(targetFields, "primitive", "target.primitive")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !stringInSlice(primitive, registry.WireActions()) {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.primitive",
			Detail: fmt.Sprintf("primitive %q is not a supported FPP action (supported: %s)", primitive, strings.Join(registry.WireActions(), ", ")),
		}
	}

	params, err := registry.DecodeActionParams(primitive, targetFields)
	if err != nil {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.params",
			Detail: err.Error(),
		}
	}

	registeredClass, ok := registry.Decision11Class(primitive)
	if !ok {
		// Unreachable given the WireActions membership check just above,
		// but answered rather than left to panic on a nil map lookup one
		// layer down if the two ever disagree.
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.primitive",
			Detail: fmt.Sprintf("primitive %q has no registered safety class", primitive),
		}
	}
	if registeredClass != declaredSafetyClass {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeSafetyClassMismatch, Field: "safetyClass",
			Detail: fmt.Sprintf("safetyClass %q does not match primitive %q's own registered safety class %q", declaredSafetyClass, primitive, registeredClass),
		}
	}

	return ShowActionTarget{
		Integration: ShowActionIntegrationFPP,
		InstanceID:  instanceID,
		Primitive:   primitive,
		Params:      params,
	}, nil
}

// decodeMQTTTarget decodes and validates target.integration == "mqtt".
func decodeMQTTTarget(targetFields map[string]json.RawMessage, brokers []IntegrationBroker) (ShowActionTarget, *ValidationError) {
	broker, verr := decodeRequiredString(targetFields, "broker", "target.broker")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !integrationBrokerDeclared(broker, brokers) {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.broker",
			Detail: fmt.Sprintf("broker %q is not a declared integration broker", broker),
		}
	}

	publishFields, verr := decodeRequiredObject(targetFields, "publish", "target.publish")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	publish, verr := decodeMQTTPublish(publishFields)
	if verr != nil {
		return ShowActionTarget{}, verr
	}

	expectFields, verr := decodeRequiredObject(targetFields, "expect", "target.expect")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	expect, verr := decodeMQTTExpect(expectFields)
	if verr != nil {
		return ShowActionTarget{}, verr
	}

	return ShowActionTarget{
		Integration: ShowActionIntegrationMQTT,
		Broker:      broker,
		Publish:     &publish,
		Expect:      &expect,
	}, nil
}

func decodeMQTTPublish(fields map[string]json.RawMessage) (ShowActionMQTTPublish, *ValidationError) {
	topic, verr := decodeRequiredString(fields, "topic", "target.publish.topic")
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}

	payload, verr := decodeRequiredStringAllowEmpty(fields, "payload", "target.publish.payload")
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}

	qos, verr := decodeRequiredInt(fields, "qos", "target.publish.qos")
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}
	if qos < 0 || qos > 2 {
		return ShowActionMQTTPublish{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.publish.qos",
			Detail: "qos must be 0, 1, or 2",
		}
	}

	// retain: THE one deliberate exception in this file beyond
	// onFailure/onUnconfirmed — see ShowActionMQTTPublish.Retain's own doc
	// comment.
	retain, verr := decodeDefaultedBool(fields, "retain", "target.publish.retain", false)
	if verr != nil {
		return ShowActionMQTTPublish{}, verr
	}

	return ShowActionMQTTPublish{Topic: topic, Payload: payload, QoS: qos, Retain: retain}, nil
}

func decodeMQTTExpect(fields map[string]json.RawMessage) (ShowActionMQTTExpect, *ValidationError) {
	kind, verr := decodeRequiredString(fields, "kind", "target.expect.kind")
	if verr != nil {
		return ShowActionMQTTExpect{}, verr
	}
	if !mqttExpectKinds[kind] {
		return ShowActionMQTTExpect{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.expect.kind",
			Detail: "kind must be one of none, boolean, number, text, or match",
		}
	}

	_, hasTopic := fields["topic"]
	_, hasValue := fields["value"]
	_, hasDeadline := fields["deadlineSeconds"]

	if kind == MQTTExpectKindNone {
		if hasTopic || hasValue || hasDeadline {
			return ShowActionMQTTExpect{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "target.expect",
				Detail: "kind \"none\" must not supply topic, value, or deadlineSeconds",
			}
		}
		return ShowActionMQTTExpect{Kind: kind}, nil
	}

	topic, verr := decodeRequiredString(fields, "topic", "target.expect.topic")
	if verr != nil {
		return ShowActionMQTTExpect{}, verr
	}

	deadline, verr := decodeRequiredInt(fields, "deadlineSeconds", "target.expect.deadlineSeconds")
	if verr != nil {
		return ShowActionMQTTExpect{}, verr
	}
	if deadline <= 0 {
		return ShowActionMQTTExpect{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.expect.deadlineSeconds",
			Detail: "deadlineSeconds must be positive",
		}
	}
	if deadline > mqttExpectMaxDeadlineSeconds {
		return ShowActionMQTTExpect{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.expect.deadlineSeconds",
			Detail: fmt.Sprintf("deadlineSeconds must not exceed %d", mqttExpectMaxDeadlineSeconds),
		}
	}

	// value's treatment per kind is this builder's own judgment call — see
	// ShowActionMQTTExpect's doc comment and this builder's report.
	// "match" requires it (the equality target); "number" accepts it as an
	// optional equality check; "boolean" and "text" have no use for it
	// (the payload IS the boolean, or IS the recorded text), so — matching
	// this endpoint's own "supplying one is an error rather than being
	// ignored" rule for kind "none" — a present value is rejected rather
	// than silently discarded.
	//
	// Both "match" and "number" decode value as a JSON STRING — a
	// GET-then-unchanged-PUT defect, found by review after this file
	// first shipped: EncodeShowActionPayload always emits value as a
	// quoted JSON string (ShowActionMQTTExpect.Value is a Go string; see
	// that field's own doc comment for why), so a decoder that required a
	// bare JSON number for kind "number" rejected the coordinator's own
	// read output on re-save, with an error pointing at a field the
	// operator never touched. "number"'s string is additionally validated
	// with strconv.ParseFloat to confirm it is really numeric, but the
	// text itself is stored verbatim, never reformatted through a float.
	var value *string
	switch kind {
	case MQTTExpectKindMatch:
		v, verr := decodeRequiredStringAllowEmpty(fields, "value", "target.expect.value")
		if verr != nil {
			return ShowActionMQTTExpect{}, verr
		}
		value = &v
	case MQTTExpectKindNumber:
		if hasValue {
			raw := fields["value"]
			if isJSONNull(raw) {
				return ShowActionMQTTExpect{}, &ValidationError{
					Code: ValidationCodeFieldNull, Field: "target.expect.value",
					Detail: "value must not be null; omit it to accept receipt without an equality check",
				}
			}
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return ShowActionMQTTExpect{}, &ValidationError{
					Code: ValidationCodeFieldInvalid, Field: "target.expect.value",
					Detail: "value must be a JSON string that parses as a number for kind \"number\" (e.g. \"42\"), matching the shape this action is read back in",
				}
			}
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return ShowActionMQTTExpect{}, &ValidationError{
					Code: ValidationCodeFieldInvalid, Field: "target.expect.value",
					Detail: "value must parse as a number for kind \"number\"",
				}
			}
			value = &s
		}
	case MQTTExpectKindBoolean, MQTTExpectKindText:
		if hasValue {
			return ShowActionMQTTExpect{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "target.expect.value",
				Detail: fmt.Sprintf("value must not be supplied for kind %q", kind),
			}
		}
	}

	return ShowActionMQTTExpect{Kind: kind, Topic: topic, Value: value, DeadlineSeconds: deadline}, nil
}

// audioActionDeclaredSafetyClass mirrors
// resolumeActionDeclaredSafetyClass's role for the audio integration:
// stop and clear are this subsystem's "stop show/session audio" action,
// and output.mute is its blackout (audiodispatch.go's own doc comment:
// "Muting the output is this subsystem's blackout"). Every other audio
// action carries no safety class of its own.
var audioActionDeclaredSafetyClass = map[string]string{
	"audio.session.stop":  ShowSafetyClassStop,
	"audio.session.clear": ShowSafetyClassStop,
	"audio.output.mute":   ShowSafetyClassBlackout,
}

// decodeAudioTarget decodes and validates target.integration == "audio".
// It does not check audioNodeId/audioSessionId against a live audio.node
// or audio.session object: this package has no store access (this file's
// own top doc comment), and unlike an fpp instance id or a resolume
// composition reference, an audio session id is minted by the caller
// (night-session driver or operator), not looked up — nightcue_readiness.go
// and the coordinator's own dispatch path are where a missing node or
// session surfaces, exactly as an unresolvable mqtt broker would if it
// were removed after an action was bound to it.
func decodeAudioTarget(targetFields map[string]json.RawMessage, declaredSafetyClass string) (ShowActionTarget, *ValidationError) {
	nodeID, verr := decodeRequiredString(targetFields, "audioNodeId", "target.audioNodeId")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	sessionID, verr := decodeRequiredString(targetFields, "audioSessionId", "target.audioSessionId")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	action, verr := decodeRequiredString(targetFields, "audioAction", "target.audioAction")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !showActionAudioActionSet[action] {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "target.audioAction",
			Detail: fmt.Sprintf("audioAction %q is not a supported audio operation (supported: %s)", action, strings.Join(showActionAudioActions, ", ")),
		}
	}

	var params map[string]any
	if raw, present := targetFields["params"]; present {
		if isJSONNull(raw) {
			return ShowActionTarget{}, &ValidationError{
				Code: ValidationCodeFieldNull, Field: "target.params",
				Detail: "target.params must not be null; omit it for an operation with no params",
			}
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			return ShowActionTarget{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "target.params",
				Detail: "target.params must be a JSON object",
			}
		}
	}

	if verr := validateAudioTargetGainParams(action, params); verr != nil {
		return ShowActionTarget{}, verr
	}

	registeredClass := audioActionDeclaredSafetyClass[action]
	if registeredClass == "" {
		registeredClass = ShowSafetyClassNone
	}
	if registeredClass != declaredSafetyClass {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeSafetyClassMismatch, Field: "safetyClass",
			Detail: fmt.Sprintf("safetyClass %q does not match audio action %q's own required safety class %q", declaredSafetyClass, action, registeredClass),
		}
	}

	return ShowActionTarget{
		Integration: ShowActionIntegrationAudio, AudioNodeID: nodeID, AudioSessionID: sessionID,
		AudioAction: action, Params: params,
	}, nil
}

// showActionGainDbParams names, per audio action, the decibel parameter
// an authored target carries and the pre-decibel name it replaced.
var showActionGainDbParams = map[string]struct{ dbKey, retiredKey string }{
	"audio.gain.set":  {dbKey: "gainDb", retiredKey: "gain"},
	"audio.gain.fade": {dbKey: "targetGainDb", retiredKey: "targetGain"},
}

// validateAudioTargetGainParams holds an authored audio.gain.* target to
// the same unit every other operator surface uses: decibels, 0 dB unity,
// at most [audio.MaxOperatorGainDb]. target.params is otherwise opaque
// here (the node validates it), but a gain is the one member whose unit
// changed, and the two units share a number range: a target still
// carrying the pre-decibel name would dispatch a halving as a
// half-decibel lift, so it is refused at AUTHORING time rather than
// discovered mid-show when the cue fires.
func validateAudioTargetGainParams(action string, params map[string]any) *ValidationError {
	names, ok := showActionGainDbParams[action]
	if !ok {
		return nil
	}
	if _, present := params[names.retiredKey]; present {
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.params." + names.retiredKey,
			Detail: fmt.Sprintf(
				"target.params.%s was a linear amplitude multiplier and no longer exists; use %s, in decibels (0 dB is unity, %g dB is silence)",
				names.retiredKey, names.dbKey, audio.SilenceFloorDb),
		}
	}
	raw, present := params[names.dbKey]
	if !present {
		return &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "target.params." + names.dbKey,
			Detail: fmt.Sprintf("target.params.%s is required for %s, in decibels (0 dB is unity, %g dB is silence)",
				names.dbKey, action, audio.SilenceFloorDb),
		}
	}
	db, ok := raw.(float64)
	if !ok {
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.params." + names.dbKey,
			Detail: fmt.Sprintf("target.params.%s must be a JSON number in decibels, got %T", names.dbKey, raw),
		}
	}
	if db > audio.MaxOperatorGainDb {
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.params." + names.dbKey,
			Detail: fmt.Sprintf("target.params.%s is in decibels and must not exceed %g dB: a larger value is a typo, not a level",
				names.dbKey, audio.MaxOperatorGainDb),
		}
	}
	return nil
}

// decodeResolumeTarget decodes and validates target.integration ==
// "resolume": reject an unrecognized action, reject unknown ref keys,
// apply the conditional rules, resolve every reference against the
// currently stored composition, then enforce the safety class.
func decodeResolumeTarget(targetFields map[string]json.RawMessage, declaredSafetyClass string, resolver ResolumeReferenceResolver) (ShowActionTarget, *ValidationError) {
	action, verr := decodeRequiredString(targetFields, "action", "target.action")
	if verr != nil {
		return ShowActionTarget{}, verr
	}
	if !showActionResolumeActions[action] {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.action",
			Detail: fmt.Sprintf("action must be one of %s", strings.Join(showActionResolumeActionNames, ", ")),
		}
	}

	refFields, verr := decodeResolumeTargetRefFields(targetFields, action)
	if verr != nil {
		return ShowActionTarget{}, verr
	}

	ref, verr := decodeResolumeRef(action, refFields)
	if verr != nil {
		return ShowActionTarget{}, verr
	}

	if verr := resolveResolumeRef(action, ref, resolver); verr != nil {
		return ShowActionTarget{}, verr
	}

	registeredClass, ok := resolumeActionDeclaredSafetyClass[action]
	if !ok {
		// Unreachable given showActionResolumeActions' own membership check
		// above, answered rather than left to compare against "" if the two
		// ever disagree.
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.action",
			Detail: fmt.Sprintf("action %q has no registered safety class", action),
		}
	}
	if registeredClass != declaredSafetyClass {
		return ShowActionTarget{}, &ValidationError{
			Code: ValidationCodeSafetyClassMismatch, Field: "safetyClass",
			Detail: fmt.Sprintf("safetyClass %q does not match resolume action %q's own required safety class %q", declaredSafetyClass, action, registeredClass),
		}
	}

	return ShowActionTarget{Integration: ShowActionIntegrationResolume, Action: action, Ref: ref}, nil
}

// decodeResolumeTargetRefFields reads target.ref for action. Required and
// non-null for every action whose own vocabulary is non-empty; for
// blackout (the one zero-key action) it must be absent or an empty
// object — requiring a key that can only ever be empty made a stored
// blackout action unable to be read back and re-PUT, since encoding an
// empty ref map drops the key (json:"ref,omitempty") and the decoder then
// saw it as missing.
func decodeResolumeTargetRefFields(targetFields map[string]json.RawMessage, action string) (map[string]json.RawMessage, *ValidationError) {
	vocab := resolumeActionRefVocabulary[action]
	raw, present := targetFields["ref"]
	if !present {
		if len(vocab) == 0 {
			return map[string]json.RawMessage{}, nil
		}
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: "target.ref", Detail: "target.ref is required"}
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: "target.ref", Detail: "target.ref must not be null"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "target.ref", Detail: "target.ref must be a JSON object"}
	}
	if len(vocab) == 0 && len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, &ValidationError{
			Code: ValidationCodeFieldUnknownKey, Field: "target.ref",
			Detail: fmt.Sprintf("action %q takes no ref keys, but ref named: %s", action, strings.Join(keys, ", ")),
		}
	}
	return fields, nil
}

// decodeResolumeRef decodes and validates target.ref against action's own
// vocabulary: an unrecognized key is rejected first, naming what this
// action accepts, before any per-key decode.
func decodeResolumeRef(action string, fields map[string]json.RawMessage) (map[string]any, *ValidationError) {
	params := resolumeActionRefVocabulary[action]

	known := make(map[string]bool, len(params))
	for _, p := range params {
		known[p.Name] = true
	}
	var unknown []string
	for k := range fields {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		expected := make([]string, 0, len(params))
		for _, p := range params {
			expected = append(expected, p.Name)
		}
		sort.Strings(expected)
		return nil, &ValidationError{
			Code: ValidationCodeFieldUnknownKey, Field: "target.ref",
			Detail: fmt.Sprintf("ref contains unrecognized key(s) for action %q: %s (this action accepts: %s)",
				action, strings.Join(unknown, ", "), formatExpectedKeys(expected)),
		}
	}

	out := make(map[string]any, len(params))
	for _, p := range params {
		raw, present := fields[p.Name]
		field := "target.ref." + p.Name
		switch {
		case !present:
			if p.Required {
				return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
			}
		case isJSONNull(raw):
			return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
		default:
			val, verr := decodeResolumeRefValue(p, raw, field)
			if verr != nil {
				return nil, verr
			}
			out[p.Name] = val
		}
	}

	if verr := validateResolumeRefConditionals(action, out); verr != nil {
		return nil, verr
	}

	return out, nil
}

func formatExpectedKeys(expected []string) string {
	if len(expected) == 0 {
		return "(none — this action takes no ref keys)"
	}
	return strings.Join(expected, ", ")
}

// decodeResolumeRefValue decodes one present, non-null ref value against
// its own declared kind.
func decodeResolumeRefValue(p resolumeRefParam, raw json.RawMessage, field string) (any, *ValidationError) {
	switch p.Kind {
	case resolumeRefString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
		}
		if s == "" {
			return nil, &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be empty", field)}
		}
		return s, nil
	case resolumeRefBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a boolean", field)}
		}
		return b, nil
	case resolumeRefNumber:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a number", field)}
		}
		return f, nil
	default:
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: "unsupported ref value kind"}
	}
}

// validateResolumeRefConditionals applies launchClip's own conditional rule
// (ADR-032 decision 6): "deck" is required unless "persistent" is true,
// and forbidden when it is. No other action in this vocabulary has a
// conditional key.
func validateResolumeRefConditionals(action string, ref map[string]any) *ValidationError {
	if action != ShowActionResolumeLaunchClip {
		return nil
	}
	_, hasDeck := ref["deck"]
	persistent, _ := ref["persistent"].(bool)
	switch {
	case persistent && hasDeck:
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "target.ref.deck",
			Detail: `target.ref.deck must not be present when target.ref.persistent is true`,
		}
	case !persistent && !hasDeck:
		return &ValidationError{
			Code: ValidationCodeFieldRequired, Field: "target.ref.deck",
			Detail: `target.ref.deck is required unless target.ref.persistent is true`,
		}
	}
	return nil
}

// ErrResolumeActionResolutionUnrecognized is [ResolveResolumeRef]'s own
// sentinel for an action name this package's resolution switch does not
// recognize. Reachable in practice only if a caller's own membership
// check (showActionResolumeActions at write time) and this switch ever
// disagree — the exact condition E7-2's own binding check must tell apart
// from "the reference does not resolve": an unrecognized action is
// "cannot check", never "checked and broken" (ADR-011).
var ErrResolumeActionResolutionUnrecognized = errors.New("config: no resolume resolution rule for this action")

// ResolveResolumeRef resolves action's own reference fields out of ref
// against resolver — the single place this package (and any other caller,
// e.g. the binding check in internal/coordinator/api) dispatches a
// resolume action name onto [ResolumeReferenceResolver]'s four methods.
// blackout resolves nothing (it addresses every tracked layer, not a
// named one). nil means resolved; every other error, including
// [ErrResolumeCompositionNotUploaded], is returned unchanged from the
// resolver, or [ErrResolumeActionResolutionUnrecognized] for an action
// this switch does not recognize.
func ResolveResolumeRef(action string, ref map[string]any, resolver ResolumeReferenceResolver) error {
	switch action {
	case ShowActionResolumeBlackout:
		return nil
	case ShowActionResolumeLaunchClip:
		clip, _ := ref["clip"].(string)
		deck, _ := ref["deck"].(string)
		layer, _ := ref["layer"].(string)
		persistent, _ := ref["persistent"].(bool)
		return resolver.ResolveClip(ResolumeClipReference{Clip: clip, Deck: deck, Persistent: persistent, Layer: layer})
	case ShowActionResolumeClearLayer, ShowActionResolumeSetLayerBypass, ShowActionResolumeSetLayerMaster:
		layer, _ := ref["layer"].(string)
		return resolver.ResolveLayer(layer)
	case ShowActionResolumeLaunchColumn:
		deck, _ := ref["deck"].(string)
		column, _ := ref["column"].(string)
		return resolver.ResolveColumn(deck, column)
	case ShowActionResolumeSelectDeck:
		deck, _ := ref["deck"].(string)
		return resolver.ResolveDeck(deck)
	default:
		return fmt.Errorf("%w: action %q", ErrResolumeActionResolutionUnrecognized, action)
	}
}

// resolveResolumeRef is [ResolveResolumeRef] wrapped for this package's
// own write-time ValidationError shape. Every non-nil error — not found,
// ambiguous, unrecognized, or [ErrResolumeCompositionNotUploaded] — is
// reported through the same Code, ValidationCodeFieldUnknownReference,
// told apart for the operator by Detail's own text rather than by a
// client branching on a second Code.
func resolveResolumeRef(action string, ref map[string]any, resolver ResolumeReferenceResolver) *ValidationError {
	err := ResolveResolumeRef(action, ref, resolver)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrResolumeActionResolutionUnrecognized) {
		// Unreachable given showActionResolumeActions' own membership
		// check in decodeResolumeTarget, answered rather than silently
		// resolving nothing if the two ever disagree.
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: "target.action", Detail: fmt.Sprintf("no resolution rule for action %q", action)}
	}
	return &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: "target.ref", Detail: err.Error()}
}

func fppInstanceConfigured(id string, endpoints []FPPEndpoint) bool {
	for _, ep := range endpoints {
		if ep.ID == id {
			return true
		}
	}
	return false
}

func integrationBrokerDeclared(id string, brokers []IntegrationBroker) bool {
	for _, b := range brokers {
		if b.ID == id {
			return true
		}
	}
	return false
}

func stringInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// validateShowRef format-validates only; existence is the caller's
// showExists check (DecodeShowActionPayload, DecodeShowMacroPayload).
// Reuses [mqttproto.ValidateNodeID] rather than inventing a second
// identifier grammar: this builder's own judgment call, since neither the
// shared contract nor STEP-9-SPEC.md states an exact format — see this
// builder's report. It happens to accept every example in STEP-9-SPEC.md
// ("halloween-2026").
func validateShowRef(show string) *ValidationError {
	if err := mqttproto.ValidateNodeID(show); err != nil {
		return &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "show",
			Detail: "show must be 1-64 characters of lowercase letters, digits, and hyphens, and must not start or end with a hyphen",
		}
	}
	return nil
}

// --- Shared low-level JSON decode helpers, used by this file and
// showmacro.go. Every one enforces the absent/null/empty distinction this
// step's payloads require (CLAUDE.md's own standing rule, restated at every
// write surface this project has shipped) rather than letting Go's zero
// value stand in for "not decided". ---

// isJSONNull reports whether raw is the literal JSON null token. Mirrors
// internal/coordinator/api's identical, unexported isJSONNull
// (fppcommand_primitives.go) — that copy is private to its own package and
// this package cannot import it (see FPPPrimitiveRegistry's own doc
// comment), so this is a second, intentionally identical implementation of
// a two-line function, not a drifting duplicate of anything with real
// logic in it.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// decodeTopLevelObject parses raw as a JSON object of raw fields, the entry
// point every Decode* function in this file and showmacro.go starts from.
func decodeTopLevelObject(raw string) (map[string]json.RawMessage, *ValidationError) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil, &ValidationError{
			Code:   ValidationCodeBodyInvalid,
			Detail: "the request body must be a JSON object",
		}
	}
	return top, nil
}

// rejectUnknownTopLevelKeys reports every key in top that is not in known,
// or nil if there are none. Both DecodeShowActionPayload and
// DecodeShowMacroPayload call this immediately after decodeTopLevelObject,
// before any per-field decode — mirroring decodeFPPCommandParams' own
// documented ordering rule (fppcommand_primitives.go: the unknown-key
// sweep runs BEFORE the per-field loop) for the identical reason: running
// it after would let a misspelled REQUIRED key be reported as "absent"
// instead of "unrecognized", which points the operator at the wrong
// remedy (add a field that is not missing, rather than fix its spelling).
// See ValidationCodeFieldUnknownKey's own doc comment for why this check
// exists at all.
func rejectUnknownTopLevelKeys(top map[string]json.RawMessage, known map[string]bool) *ValidationError {
	var unknown []string
	for k := range top {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return &ValidationError{
		Code: ValidationCodeFieldUnknownKey,
		Detail: fmt.Sprintf(
			"the payload contains unrecognized key(s): %s (a typo'd key is refused rather than silently applying that field's own default or being ignored)",
			strings.Join(unknown, ", ")),
	}
}

// decodeRequiredObject reads key from top as a required, non-null JSON
// object.
func decodeRequiredObject(top map[string]json.RawMessage, key, field string) (map[string]json.RawMessage, *ValidationError) {
	raw, present := top[key]
	if !present {
		return nil, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return nil, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON object", field)}
	}
	return fields, nil
}

// decodeRequiredString reads key from top as a required, non-null,
// non-empty string.
func decodeRequiredString(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	if s == "" {
		return "", &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be empty", field)}
	}
	return s, nil
}

// decodeRequiredStringAllowEmpty is [decodeRequiredString] without the
// empty-string rejection, for a field where an explicit empty string is a
// real, distinct, valid value (an MQTT payload, or a "match" target value)
// rather than a stand-in for "nothing was provided".
func decodeRequiredStringAllowEmpty(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	return s, nil
}

// decodeOptionalString reads key from top: absent means "" (unset);
// present-and-null is an error; present is the value verbatim, including
// "" (an operator explicitly clearing a description is indistinguishable
// on the wire from never having set one, and nothing downstream needs the
// two told apart the way onFailure/onUnconfirmed's defaults must be).
func decodeOptionalString(top map[string]json.RawMessage, key, field string) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return "", nil
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to leave it unset", field)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	return s, nil
}

// decodeRequiredInt reads key from top as a required, non-null whole
// number. It decodes as json.Number and parses with strconv.ParseInt
// rather than round-tripping through float64: a float64 guard
// (`f != float64(int(f))`) cannot detect a JSON integer literal outside
// float64's exactly-representable range — 9007199254740993 decodes to
// 9007199254740992 and the round trip compares equal — and above
// math.MaxInt64 the float64->int conversion saturates instead of
// overflowing visibly. json.Number preserves the literal's exact decimal
// text, so a non-integer, an out-of-range value, or too many digits is
// refused outright instead of silently returning a mangled value.
func decodeRequiredInt(top map[string]json.RawMessage, key, field string) (int, *ValidationError) {
	raw, present := top[key]
	if !present {
		return 0, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return 0, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON number", field)}
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a whole number", field)}
	}
	if v < math.MinInt || v > math.MaxInt {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a whole number", field)}
	}
	return int(v), nil
}

// decodeDefaultedBool reads key from top: absent takes def; present-and-null
// is always an error (never "use the default"); present is the value
// verbatim.
func decodeDefaultedBool(top map[string]json.RawMessage, key, field string, def bool) (bool, *ValidationError) {
	raw, present := top[key]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return false, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default (%v)", field, def)}
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a boolean", field)}
	}
	return b, nil
}

// decodeDefaultedEnum reads key from top: absent takes def; present-and-null
// is always an error; present-and-empty-string is always an error (distinct
// from absent — CLAUDE.md's own standing rule for this step, restated
// explicitly rather than left to fall out of a zero value); present must be
// a member of allowed. This is the ONE function onFailure and onUnconfirmed
// both go through (showmacro.go), each with its own field name, its own
// default, and its own allowed set — deliberately independent calls rather
// than one shared piece of state, so the two policy axes STEP-9-SPEC.md
// section 2.2 / ADR-031 decision 2 requires stay genuinely separate and
// this function cannot become "the one field that answers both".
func decodeDefaultedEnum(top map[string]json.RawMessage, key, field, def string, allowed map[string]bool) (string, *ValidationError) {
	raw, present := top[key]
	if !present {
		return def, nil
	}
	if isJSONNull(raw) {
		return "", &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null; omit it to use the default (%q)", field, def)}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a string", field)}
	}
	if s == "" {
		return "", &ValidationError{Code: ValidationCodeFieldEmpty, Field: field, Detail: fmt.Sprintf("%s must not be an empty string; omit it to use the default (%q)", field, def)}
	}
	if !allowed[s] {
		return "", &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s is not a recognized value", field)}
	}
	return s, nil
}
