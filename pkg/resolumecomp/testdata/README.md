# resolumecomp testdata

Every fixture in this directory is **synthetic**, hand-authored against the
`.avc` structure recorded in the bench capture this package's parser
implements. No operator composition file was copied, in whole or in part,
into any file here.

The operator's own composition files live outside this repository, at
`~/Documents/Resolume Arena/Compositions/`. This repository is public, and
those files carry the operator's real clip names and absolute media paths.
Clip names, media paths, and composition names in these fixtures
(`Clip A`, `Snowflakes`, `Deck One`, `/media/example/clip-a.mov`, and
similar) are placeholders invented for these tests and do not correspond to
anything in a real show.

## What was substituted, and what was not

**Structure was measured; content was invented.** The element and attribute
shapes below — which elements carry which attributes, where a name
attribute is a literal placeholder like `"Clip"` rather than the operator's
own name, where `PersistentClips` sits relative to `Deck`, which attributes
are pointer-typed because the file omits them rather than zeroing them —
match what was measured from real files during the bench capture. The
`uniqueId` values, clip and deck names, and media paths in every fixture
here are fabricated and follow no numbering scheme from any real file.

## The rule these files exist to enforce

**A failing test here means the parser is wrong, not the fixture.** These
files encode the structural facts the parser is built against. If a future
Resolume version changes the file format, that needs a new fixture built
from the new structure and a note saying so, not an edit that quietly makes
an old assumption pass.

## Fixtures

- `complete.avc` — a small two-deck composition exercising deck name
  joining, the clip-name trap (attribute name is the literal `"Clip"`,
  the real name is in a nested param), empty clip slot exclusion, group
  membership including a layer with no group at all, duplicate `Column`
  elements sharing one `columnIndex`, and two persistent clips.
- `not-xml.txt` — not XML at all.
- `wrong-root.avc` — well-formed XML whose root element is not
  `<Composition>`.
- `missing-compositioninfo.avc` — a `<Composition>` with no
  `<CompositionInfo>` child.
- `bad-layerindex.avc` — a clip whose `layerIndex` attribute is present but
  not numeric.
- `missing-layerindex.avc` — a clip whose `layerIndex` attribute is absent
  entirely.
- `deck-missing-id.avc` — a `<Deck>` (and its matching `<DeckInfo>`) whose
  `uniqueId` is empty. Review finding C: this must reject the whole parse
  (`ErrMissingDeckID`), not silently produce a deck clip with an empty
  `DeckID`, which is the wire encoding reserved exclusively for a
  persistent clip.
- `malformed-index-empty-slot.avc` — one deck with two `<Clip>` elements:
  the first is an EMPTY slot (no children) whose `layerIndex` is also
  non-numeric, the second is an ordinary non-empty clip. Review finding I:
  the malformed index on the empty slot must not reject the file — real
  compositions are mostly empty slots (226 of 252 measured in the bench
  capture) — so parsing must succeed with exactly the second clip surviving.
