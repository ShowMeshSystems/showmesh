# RES-003: xLights and FPP Connect Compatibility

[Architecture](../architecture/ARCHITECTURE.md#3-system-architecture) · [Tracker](README.md) · [MultiSync research](RES-002-fpp-multisync-compatibility.md)

Status: planned (renderer delivery decided; integration L0) · Risk: high · Verification: L0

## Decision to make

Validate the least disruptive workflow for exporting, naming, and delivering media-node assets while preserving normal xLights and FPP Connect use. The renderer boundary is settled: FPP Connect uploads the sequence and FSEQ to each renderer node ahead of playback, and the renderer extracts its assigned virtual-matrix channels locally.

## Questions

- Which supported FPP Connect mechanism can deliver the sequence and FSEQ to a renderer node without an xLights fork?
- Which additional media, model, or manifest artifacts does the renderer workflow require?
- Can one virtual matrix be produced per logical surface, including both one-surface-per-projector and combined-surface layouts?
- Are headless or batch exports available and deterministic?
- Which metadata is needed to relate exported media to sequence duration, frame rate, surface, and checksum?
- Where can validation and transcoding occur without changing xLights?

## Acceptance criteria

- A representative show can be prepared through a documented repeatable workflow.
- Existing FPP targets continue to use normal FPP Connect behavior.
- FPP Connect delivers the sequence and FSEQ to the renderer node before playback; playback requires no live matrix stream or coordinator asset transfer.
- Media-node artifacts have deterministic names, dimensions, duration, frame rate, and hashes.
- One content update can be validated and delivered without manual file hunting.

## Test method

Record xLights and FPP versions and use representative logical-surface layouts, including one surface per projector and a combined canvas. Exercise the supported FPP Connect delivery path to a renderer node, inspect the resulting local files, and prototype an external manifest/validation helper before considering upstream changes.

## Evidence and findings

The renderer delivery boundary was settled by the project owner on 2026-08-13. It is workflow intent rather than observed FPP Connect behavior, so this record remains L0 until the delivery path is exercised and recorded.

These decisions are recorded as durable constraints in [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md), which also fixes the rule that the reference profile is described as intended rather than supported until this record's bench runs.

## Decision, fallback, and revalidation

Use FPP Connect to upload the sequence and FSEQ to renderer nodes ahead of playback. The fallback is documented manual export plus direct node upload. Any proposed xLights modification must demonstrate that an external helper cannot provide the same result. Revalidate after material xLights or FPP Connect changes.
