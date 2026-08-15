# Resolume REST fixtures

These are **hand-built, synthetic fixtures**, not raw captures.

## Files

- `product.json` — `GET /api/v1/product`'s exact captured body (provenance
  table).

## The rule these files exist to enforce

Same rule as `internal/coordinator/collector/fpp/testdata/README.md`'s: a
failing test here means the decoder is wrong, not the fixture. Every field
shape in `product.json` is traceable to
`docs/bench/resolume-control-surface.md`; if Resolume's real behaviour has
genuinely changed, that needs a new capture and a note saying so, not an
edit to make a test pass.

## What used to be here

`composition_minimal.json` and `composition_minimal_restarted.json`
fixtured a full `GET /composition` decode (object/parameter id
resolution, restart fingerprinting). That call is now forbidden at
runtime — `GET /composition` measured crashing the target Arena build —
and the decode tree those fixtures exercised was deleted along with it.
Nothing in this package decodes a full composition document anymore, so
there is nothing left for those fixtures to feed.
