// Package multisync will contain the FPP MultiSync wire protocol: a UDP
// listener and packet types for START/STOP/SYNC/OPEN and ping/discover
// messages, per the byte layout recorded in docs/research/RES-002.
//
// It is not yet implemented. It lands in Step 1 of the build order (see
// CLAUDE.md), starting from the RES-002 byte layout and bench-verified
// against a real FPP player per that record's open items.
package multisync
