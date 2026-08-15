// Package resolume is the Resolume Arena REST adapter's read-only
// observation layer: a REST client for Arena's `/api/v1` surface (Track D
// seam D-1), the stored composition id map (seam D-2/A, D-2/B), and the
// composition survey — layer readiness, composition identity, and the
// rest of the signal vocabulary — built on top of both (seam D-2/C). It
// implements internal/coordinator/collector.Collector.
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
// Through seam D-2 this package issued no action against Resolume at all:
// no POST, PUT, or DELETE, every exported Client method a GET. Track D seam
// D-3 (action.go and its siblings) is the first code in this package
// permitted to change anything on the wall, and it does so through a fixed,
// closed vocabulary of exactly seven actions (TRACK-D-D3-SPEC.md §2) —
// launch a clip, clear a layer, blackout, launch a column, select a deck,
// and set a layer's bypass or master parameter — each dispatched by object
// id through new Client methods D-3 adds in its own files, never by editing
// client.go's original GET-only methods. `POST /composition/action`
// (undo/redo) and every `DELETE` remain permanently outside that
// vocabulary: D-3's own specification excludes them outright, and nothing
// in this package's Client has a method that could issue either. No method
// anywhere in this package, before or after D-3, performs GET /composition
// — see this comment's own section above, which D-3 does not narrow.
// Client's CheckRedirect refuses every redirect unconditionally regardless
// of HTTP method — the same defence internal/coordinator/collector/fpp
// uses, for the identical reason: Resolume's REST API serves destructive
// calls on the same host as everything this package reads, so a
// coordinator that silently followed a 3xx could be turned into a confused
// deputy issuing one of them.
//
// The layer-readiness conjunction ([LayerReady], readiness.go) and the
// composition-identity check ([CheckIdentity], identity.go) DO live in
// this package as of seam D-2/C, each computed in exactly one function —
// see those two files' own doc comments. What still does not live here is
// how often either one runs: [Collector.Poll]'s steady-state timer only
// ever performs the two-signal liveness probe D-1 shipped; a survey (the
// only thing that produces readiness or identity evidence) runs
// exclusively via [Collector.RequestSurvey] — an explicit request or a
// confirmed WebSocket reconnect, never on a timer.
//
// The WebSocket change signal (built in this package's watch.go) is a
// wake-up, never an authority: a message on it means "something may have
// changed," and it is never itself the source of an observed value. A
// connect wakes resolumewiring.go's own OnConnect callback, which calls
// [Collector.RequestSurvey]; an ordinary change message still only ever
// triggers an immediate `/product` poll (see resolumewiring.go's own
// comment on OnChange). No observed value is ever read out of a WebSocket
// message on either path.
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
