// Package command holds ARCHITECTURE section 8.1's shared command
// envelope model: the vocabulary a write endpoint uses to represent one
// primitive command before it is stored, dispatched, and confirmed.
//
// This is a pure data model: no HTTP client, no FPP knowledge, no
// database access. Step 7 seam C is its first real caller —
// internal/coordinator/api builds an [Envelope] from an incoming request
// and a resolved principal, then maps it onto its own storage-shaped
// record ([internal/coordinator/store.CommandRecord]) — and
// internal/coordinator/fppcommand is the separate package that actually
// speaks to FPP; neither of those concerns belongs here.
//
// # Idempotency keys are minted by the caller, never assumed
//
// RES-015 section 7.3 established that nothing in FPP's command path
// carries an invocation identity FPP itself could supply as one: not the
// command interface, not its result type, not the cross-host wire packet.
// So ARCHITECTURE section 8.1's "every command carries ... an idempotency
// key" is an obligation on whoever is issuing the command, not on FPP.
// [NewIdempotencyKey] is that minting, called once per invocation by
// every issuer this codebase has today: the coordinator's own FPP command
// endpoint (for a request that arrived with none) and showmeshctl (which
// mints its own before the request ever leaves the process, so a retried
// HTTP call reuses it rather than minting a second one).
package command
