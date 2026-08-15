// Package resolume is the Resolume Arena REST adapter's read-only
// foundation (Track D, seam D-1): a REST client for Arena's `/api/v1`
// surface and a two-signal reachability collector implementing
// internal/coordinator/collector.Collector.
//
// # No runtime path may call GET /composition
//
// Not on connect, not on a timer, not on a change signal, not to verify
// anything. Measured live against the operator's own Arena: `GET
// /composition` crashed it outright — SIGSEGV, byte-identical faulting
// frames across seven separate reproductions, including once from `curl`
// alone with no ShowMesh process running, which is what rules this
// package out as the cause. Two reads, thirty seconds apart, were enough.
// This package therefore has NO method that performs that read — see
// [Client]'s own doc comment — and guardfullcomposition_test.go enforces
// the prohibition mechanically: it fails the build if any non-test file
// in this package's directory constructs that request path again.
//
// The object-id resolution this package used to perform by reading
// `/composition` (Track D seam D-1's first cut) is gone with the read
// that produced it. What replaces it — a stored id map sourced from the
// operator's composition file, and a `by-id` read for anything live — is
// a later seam's job, not this package's. See composition.go's own doc
// comment for exactly which pieces of the old decode survive as building
// blocks for that later seam, and why.
//
// # What this package deliberately is not
//
// It is not an OSC client, in either direction. Arena's default OSC
// address space is positional-only and cannot name a pinned clip, and
// its output stream carries index values decodable only via a REST read
// anyway — so REST is the only transport this package, or any Resolume
// adapter seam, speaks. Nothing in this package imports or writes any OSC
// wire format.
//
// It issues no action against Resolume: no POST, PUT, or DELETE. Every
// exported Client method is a GET, and Client's CheckRedirect refuses
// every redirect unconditionally — the same defence
// internal/coordinator/collector/fpp uses, for the identical reason even
// though this package's own requests are all GETs: Resolume's REST API
// also serves destructive POSTs and DELETEs on the same host, so a
// coordinator that silently followed a 3xx could be turned into a
// confused deputy issuing one of those. Actions, and the confirmation
// vocabulary built on top of this foundation, are a later seam (D-3).
//
// It knows nothing about composition semantics. No layer-readiness
// conjunction, no composition-identity assertion, no "is this layer
// putting anything on the wall" logic lives here — that is a later seam.
// This package reports whether Arena answered at all; it does not
// interpret what a composition contains.
//
// The WebSocket change signal (built in this package's watch.go) is a
// wake-up, never an authority: a message on it means "something may have
// changed," and it is never itself the source of an observed value.
// Today that wake-up only ever triggers an immediate `/product` poll
// (see resolumewiring.go's own comment on OnChange) — it does not, and
// while the /composition prohibition above holds, cannot, trigger a
// re-resolution of any composition state. Nothing in this file's half of
// the package reads from or depends on the WebSocket.
//
// # The parameter-id lifecycle rule
//
// Resolume hands out two different kinds of identifier. Object ids
// (clips, layers, layer groups, columns, decks) are stored in the
// composition file and survive a restart, a reorder, and a multi-year
// rebuild of the show. Parameter ids — the id carried on every
// `{"valuetype":...,"id":...,"value":...}` leaf, used to address a single
// property over REST (`/parameter/by-id/{id}`) or subscribe to it over
// the WebSocket — are minted fresh every time Arena loads a composition,
// including on every restart, and nothing announces the change: a
// subscription keyed on a dead parameter id does not error, it simply
// goes quiet forever.
//
// The consequence is structural, not a convention this package's callers
// have to remember: [ParameterID] cannot be marshaled to JSON at all —
// [ParameterID.MarshalJSON] always returns an error — so any attempt to
// write one into an API response, a config revision, an export bundle, or
// any other JSON this project produces fails loudly, at the point of the
// mistake, rather than shipping a value that silently stops meaning
// anything the next time Arena restarts. [ParameterID.String] is still
// available, deliberately, for a log line: a log is not a persistence or
// wire boundary, and a maintainer reading a log benefits from seeing the
// id even though nothing downstream may ever store it.
package resolume
