# Resolume REST fixtures

These are **hand-built, synthetic fixtures**, not raw captures. The real
`GET /composition` body the bench capture measured is ~2.26 MB on one
operator composition (18 layers, 3 layer groups, 14 columns, 3 decks, 252
clip slots) — far too large to commit and mostly irrelevant to what this
package actually decodes. What is committed instead is shaped from
`docs/bench/resolume-control-surface.md` (Arena 7.23.2, captured
2026-08-14): every field name, nesting, and value shape below is taken
directly from that document's JSON excerpts and prose, scaled down to a
composition small enough to read in one sitting.

## Files

- `product.json` — `GET /api/v1/product`'s exact captured body (provenance
  table).
- `composition_minimal.json` — a small but structurally faithful
  composition: 2 decks (one selected), 2 columns, 1 layer group (with a
  duplicated-nested-layer entry carrying extra junk fields, to exercise
  that this package decodes only that entry's `id` and discards the rest —
  capture section 4.1), and 2 layers each with a 2-clip grid. Covers every
  field name in resolve.go's closed parameter index, plus `active_clip`
  present (layer 1) and absent (layer 2, no key at all), plus
  `transport.controls` present (clip 1) and null (clip 2, 3, 4) — capture
  sections 4.3, 4.4, 8, 9.2, 9.4, 11.3.
- `composition_minimal_restarted.json` — `composition_minimal.json` with
  every parameter id shifted by +5,000,000 and every object id left
  untouched, simulating capture section 3.2's measured restart shape
  (object ids 14/14 identical, parameter ids 0/14 identical) for
  [TestResolveFingerprintsIsolateParameterIDChurn].

## The rule these files exist to enforce

Same rule as `internal/coordinator/collector/fpp/testdata/README.md`'s: a
failing test here means the decoder is wrong, not the fixture. Every field
shape in `composition_minimal.json` is traceable to
`docs/bench/resolume-control-surface.md`; if Resolume's real behaviour has
genuinely changed, that needs a new capture and a note saying so, not an
edit to make a test pass.

## What is deliberately NOT here

No `active_clip: null` case, and no `transport.controls` object with real
content, exist in these two files as committed — the former is covered by
JSON literals inline in `composition_test.go` (a one-field decode test
does not need a full composition fixture), and the latter was never
observed in the capture at all (only ever seen as `null`, under SMPTE
transport — capture section 11.3), so inventing its shape here would be
exactly the "if a field is not in the capture, do not invent it" mistake
this package's own doc comments warn against.
