# Resolume REST fixtures

These are **hand-built, synthetic fixtures**, not raw captures.

## Files

- `product.json` — `GET /api/v1/product`'s exact captured body (provenance
  table).
- `layer_present.json`, `layergroup_present.json`, `deck_present.json`,
  `clip_present.json` — Track D seam D-2's targeted `by-id` decode types,
  with every conjunction/identity leaf carrying a real value. Object ids,
  field shapes and value ranges follow capture sections 4.3, 8, 8.1, 9.2
  and 16; the composition/layer/clip **names** are invented placeholders
  ("Layer 1", "Test Clip", …), never the operator's real show content —
  see `docs/private/` confidentiality convention.
- `layer_null_terms.json`, `layergroup_null_terms.json`,
  `deck_null_terms.json`, `clip_null_terms.json` — the SAME shapes with
  every optional leaf sent as explicit JSON `null` rather than a value.
  These are what `TestLayerNullFieldsAreNeverReadAsZeroValues` and its
  siblings assert against: a `null` must decode to `PresenceNull`, never
  silently collapse to the field's Go zero value (`false`, `0`, `""`) —
  the "ma": null defect (CLAUDE.md), reproduced here for `bypassed`
  specifically because capture section 8.1 is explicit that a bypassed
  layer is the one case where reading `false` for "not bypassed" makes a
  dark layer report ready.
- `layer_absent_terms.json`, `layergroup_absent_terms.json`,
  `deck_absent_terms.json`, `clip_absent_terms.json` — the same shapes
  again with every optional leaf omitted from the document entirely
  (only `id`, and for the deck fixture, `closed`, remain). Distinct from
  the `_null_terms` fixtures on purpose: `PresenceAbsent` and
  `PresenceNull` must be two different outcomes, not one.
- `composition_bypassed.json`, `composition_master.json`,
  `composition_name.json` — rung-1 bodies for TRACK-D-D2-SPEC.md §4's
  composition-level parameter ladder (`GET /composition/bypassed`,
  `/composition/master`, `/composition/name`). There is no rung-2 fixture:
  rung 2 is an ordinary `404`, exercised in the tests with
  `httptest.Server`'s own `http.StatusNotFound`, not a fixture file.

## The rule these files exist to enforce

Same rule as `internal/coordinator/collector/fpp/testdata/README.md`'s: a
failing test here means the decoder is wrong, not the fixture. Every field
shape in these files is traceable to
`docs/bench/resolume-control-surface.md`; if Resolume's real behaviour has
genuinely changed, that needs a new capture and a note saying so, not an
edit to make a test pass. Object ids reuse the ones the capture recorded
(they are stable, non-secret integers — capture section 3.2 — not
infrastructure identity); hostnames and IP addresses never appear in these
files at all, so there is nothing here to substitute with an RFC 5737
placeholder.

## What used to be here

`composition_minimal.json` and `composition_minimal_restarted.json`
fixtured a full `GET /composition` decode (object/parameter id
resolution, restart fingerprinting). That call is now forbidden at
runtime — `GET /composition` measured crashing the target Arena build —
and the decode tree those fixtures exercised was deleted along with it.
Nothing in this package decodes a full composition document anymore, so
there is nothing left for those fixtures to feed.
