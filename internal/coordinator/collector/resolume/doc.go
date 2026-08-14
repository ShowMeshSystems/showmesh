// Package resolume is the Resolume Arena REST adapter's read-only
// foundation (Track D, seam D-1): a REST client for Arena's `/api/v1`
// surface, an object-id resolver over one fetched composition, and a
// two-signal reachability collector implementing
// internal/coordinator/collector.Collector.
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
// putting anything on the wall" logic lives here — that is seam D-2. This
// package resolves object and parameter ids and reports whether Arena
// answered at all; it does not interpret what a composition contains.
//
// The WebSocket change signal (built in this package's watch.go, by a
// parallel seam) is a wake-up, never an authority: a message on it means
// "read the REST API again now," and it is never itself the source of an
// observed value. The one authority for what Resolume's state actually is
// is a GET against `/composition` or a `by-id` resource. Nothing in this
// file's half of the package reads from or depends on the WebSocket.
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
//
// [Resolution], produced by [Resolve], holds parameter ids only in
// memory, for the lifetime of one held resolution, and is discarded
// wholesale — never diffed, never merged — on every reconnect by
// [Adapter.HandleConnect] and [Adapter.HandleDisconnect]. See
// [Resolution]'s own doc comment for the closed set of parameters this
// package actually indexes.
package resolume
