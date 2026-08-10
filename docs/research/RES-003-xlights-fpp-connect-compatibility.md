# RES-003: xLights and FPP Connect Compatibility

[Architecture](../architecture/ARCHITECTURE.md#3-system-architecture) · [Tracker](README.md) · [MultiSync research](RES-002-fpp-multisync-compatibility.md)

Status: unresearched · Risk: high · Verification: L0

## Decision to make

Define the least disruptive workflow for exporting, naming, validating, and distributing media-node assets while preserving normal xLights and FPP Connect use.

## Questions

- Which sequence, media, model, and manifest artifacts does FPP Connect currently produce or transfer?
- Can third-party nodes be represented or targeted without an xLights fork?
- Can one virtual matrix or exported surface be produced per projector?
- Are headless or batch exports available and deterministic?
- Which metadata is needed to relate exported media to sequence duration, frame rate, surface, and checksum?
- Where can validation and transcoding occur without changing xLights?

## Acceptance criteria

- A representative show can be prepared through a documented repeatable workflow.
- Existing FPP targets continue to use normal FPP Connect behavior.
- Media-node artifacts have deterministic names, dimensions, duration, frame rate, and hashes.
- One content update can be validated and delivered without manual file hunting.

## Test method

Record xLights and FPP versions and use representative matrix layouts, including one surface per projector and a combined canvas. Inspect supported export and FPP Connect behavior, then prototype an external manifest/validation helper before considering upstream changes.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

Decision pending. The fallback is documented manual export plus coordinator upload. Any proposed xLights modification must demonstrate that an external helper cannot provide the same result. Revalidate after material xLights or FPP Connect changes.
