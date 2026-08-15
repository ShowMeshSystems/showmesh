# ADR-037: A Resolume Reference Is a Name, Not an Object Id

Status: Accepted (owner decision, 2026-08-15). Not yet implemented.

Related: [ADR-029](ADR-029-logical-actions-and-integration-bindings.md) (macros invoke
logical actions, never protocol commands),
[ADR-032](ADR-032-resolume-composition-configuration-from-file.md) (the id map comes from
the composition file),
[ADR-030](ADR-030-operator-ui-is-the-authoring-surface.md) (the UI is the authoring
surface and the CLI must drive everything it can),
[ADR-027](ADR-027-show-and-surface-model.md) (the Show object).

Seam: [TRACK-D-D3-SPEC.md](../build/TRACK-D-D3-SPEC.md) §9 raised this as an open
question; this record answers it.

## Context

D-3 shipped seven Resolume actions. Each takes an `id`, and the API documents it as "the
ShowMesh reference this action resolves through the stored composition id map". Two
independent reviews found the same thing: it is not a reference. It is Arena's own numeric
object id, parsed out of the uploaded `.avc` and passed through as a decimal string.
Resolving a raw Resolume id against a map keyed on raw Resolume ids is not an indirection.

The owner's reaction settled it: *"I don't even know where I'd find those clip id
numbers."* That is the correct reaction, and it is the ADR-029 test failing in the field.
The ids are technically discoverable, from
`showmeshctl resolume composition show` or from `GET /config/resolume/composition`, but
what that prints is eighteen layers as bare eighteen-digit integers.

The deeper problem is not discovery, it is durability. ADR-029 exists so that rebinding
once keeps every macro correct. As built, re-authoring the composition mints new ids, and
every stored macro binding starts silently refusing.

## What the composition file actually contains, measured 2026-08-15

From `Christmas 25.avc`, the operator's real show, 18 layers and 36 clips:

- **Layers carry user-assigned names, and ShowMesh currently drops them.** The `<Layer>`
  element's own `name` attribute is the generic string `"Layer"` on all 18. The real name
  lives in a nested `<Param name="Name" T="STRING" value="...">`, which is the identical
  shape the parser **already reads for decks**. 13 of 18 layers have one: `Peak Only`,
  `All Front Windows`, `Whole House 1`, `Whole House 2`, `Whole Front`, `Bedroom Window`,
  `Center Window`, `Small Front Windows`, `Below PeakMessage Spaces`, `Peak + Under `,
  `Side`, `Front`. **5 have no name at all.**
- **Clip names exist but are not unique**: 36 clips carry 18 distinct names.
- **Columns carry no name attribute of any kind**, only `uniqueId` and `columnIndex`.
- **Decks are named** and already parsed: `Main`, `Rest Staging`, `Downloads`.
- Note `Peak + Under ` has a trailing space, and one clip name contains a full-width
  vertical bar. Names are operator-typed strings, not identifiers.

## Decision

**1. Every operator-facing Resolume reference is a name, not an object id.** That covers
the API request payloads, `showmeshctl` arguments, macro steps, and the Operator UI. A raw
object id never appears in any of them.

**2. Object ids remain the only thing on the wire to Arena.** ADR-032 and the package's
positional-addressing guard are unchanged. The name-to-id resolution happens once, in the
coordinator, against the stored composition. **Nothing about the adapter's REST addressing
changes.**

**3. A reference is scoped, because names are not unique.** A bare clip name cannot
identify a clip in this composition, measured. The reference carries enough to be
unambiguous, and carries the deck for clips per ADR-032 decision 6.

**4. An unnamed object gets a stable generated label, not a blank.** Five layers have no
name, and columns never do. The label is derived from position (`Layer 1`, `Column 4`) and
is presented as generated rather than authored, so an operator can tell the difference
between a name they chose and one ShowMesh invented.

**5. A reference that resolves to more than one object is an error at bind time, not a
coin flip at showtime.** Ambiguity is reported when the macro or the command is written,
naming the candidates. Taking the first match is the `precedence.go` defect this project
already caught once.

**6. A reference that no longer resolves is refused with the name stated**, never silently
skipped and never resolved to a neighbour. "The clip named X is not in the current
composition" is the message; the remedy is to re-bind or re-upload.

**7. The parser must read layer names**, from the same `<Param name="Name">` shape it
already reads deck names from, and the API and CLI must show them. This is the smallest
piece of the work and it delivers most of the day-one value, because it turns eighteen
integers into a list an operator recognises.

**8. The UI surfaces are a dropdown for macro authoring and a dropdown plus a Go button on
the controller page** (owner, 2026-08-15). Both are populated from the stored composition,
which the coordinator already holds and already serves.

## Consequences

- D-3's wire contract changes in a **non-additive** way for the `id` field. It has never
  been used by anything but this session's own bench run, no macro references it yet, and
  the UI for it does not exist. Change it now rather than carrying a compatibility shim
  for a contract with no clients. Within a major version ADR-020 requires additive change,
  so this is the moment to do it, before D-4 gives it a client.
- `showmeshctl resolume composition show` becomes genuinely useful rather than a wall of
  integers, and the ids stay visible for debugging.
- Re-authoring a composition preserves bindings as long as names are preserved, which is
  the ADR-029 property that motivated this. It does **not** survive renaming an object,
  and it should not: a rename is the operator saying this is a different thing.
- **This does not build the Show object.** ADR-027's Show is a namespace and references
  will eventually be scoped by it; this record deliberately scopes to the single stored
  composition, which is what exists today.

## Alternatives rejected

**Keep raw ids and document them honestly.** Cheapest, and it fails the owner's own test:
he could not find them, and the bindings do not survive re-authoring.

**A ShowMesh-minted opaque handle per object.** Durable across renames, and it reintroduces
the discovery problem the operator just hit: an opaque handle is an id with a different
prefix.

**Positional references (`layer 7, column 3`).** Rejected outright. ADR-032, the capture,
and this package's own AST guard all forbid positional addressing, and a position means a
different object the moment anything is reordered.

## Open

**Names in a macro are resolved at author time or at run time.** Author time makes a
rename break loudly at the next edit; run time makes it break loudly at showtime. The
answer probably differs for a macro versus a controller button, and it belongs with the
macro work in Track A rather than here.
