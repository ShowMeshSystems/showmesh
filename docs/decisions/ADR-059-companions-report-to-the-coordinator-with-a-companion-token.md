# ADR-059: Companions report to the coordinator with a companion-role token

Status: Accepted (owner, 2026-10-14)
Date: 2026-10-14

## Context

ShowMesh Companion runs beside Resolume Arena on the render host. Until now it
only read from the coordinator, through the unauthenticated night-session
route, and held no credential. Its architecture record deferred any report back
to the coordinator until the core repository had an enrollment model and
explicit schemas for one.

The first thing that needs to flow back is Arena's crash reports. Arena 7.23.2
crashes in its own web API server on the playout host, the operator relaunches
it through the companion, and the crash reports stay on that machine where
nobody can read them without a shell on it.

The owner decided on 2026-10-14 that reports go to the coordinator first, that
a forward to a remote destination may follow as a separate coordinator-side
change, and that the companion authenticates with a narrow token. The owner
also noted that the companion will need a token to send other data later, so
the credential must not be specific to crash reports.

## Decision

1. **A `companion` role exists, and it holds only what a companion's own
   reports need.** Today that is one scope, `companion:report`. A later
   companion report adds its scope to this role rather than minting a new
   role. Admin holds every companion scope because admin holds everything.
   No operator or viewer credential can forge a companion report.

2. **Enrollment is the existing principal and token flow.** An administrator
   creates a machine principal with the `companion` role and issues it a token,
   from the UI, `showmeshctl principal` and `showmeshctl token`, or the
   coordinator binary. The operator pastes the token into the companion's
   settings. No companion-specific enrollment code exists.

3. **`POST /api/v1/companion-reports` stores one file per request.** The body
   carries the companion's name, the report kind, the source application, the
   file name, the file's modification time, its SHA-256 and its text. The
   coordinator recomputes the hash and refuses a mismatch. Identical content
   from the same companion is stored once, and a repeat answers with the stored
   report, so the companion can retry without bookkeeping on the coordinator.
   Content is capped at 4 MiB. `crash_report` is the only kind.

4. **The coordinator keeps the newest 200 reports per companion.** Older rows
   are deleted as new ones arrive. Reading them back is an observation read:
   `GET /api/v1/companion-reports` lists summaries and
   `GET /api/v1/companion-reports/{reportId}` returns the content, with
   `showmeshctl companion-reports list|get` at parity.

5. **Forwarding reports off the show LAN is not part of this decision.** If it
   is built, the coordinator forwards. The companion never holds a credential
   for a remote service.

## Consequences

- The show machine gains one outbound HTTP call to the coordinator, on the
  companion's own thread, and nothing inbound.
- Reads follow the observation posture: open on a coordinator that has not
  closed reads. A crash report names user paths and the host's hardware, which
  is within what the isolated show LAN already exposes.
- The companion's crash-report collection, its ledger of what it already sent,
  and its platform support are the companion repository's own decisions.
