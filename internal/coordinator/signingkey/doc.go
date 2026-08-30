// Package signingkey owns the coordinator's Ed25519 signing keypair for
// its entire lifecycle on this side of the trust boundary ADR-025
// establishes: generation at first run, on-disk persistence under the
// coordinator's data directory, and signing (decisions 1 and 2). The
// private key never leaves the coordinator's data volume and is never
// authored into this repository or shipped in a deploy bundle.
//
// This package holds NO verification logic. ADR-025's verify function
// must be callable identically by this coordinator and by internal/agent
// (Track J seam J3), and internal/agent must never import
// internal/coordinator — see pkg/coordsig's package comment for the
// shared package both sides depend on instead, the same role
// pkg/cuecatalog and pkg/fppidentity already play for other
// coordinator/agent boundaries.
//
// Node-side key pinning, the node key set, key rotation, and enrollment
// delivery of the public key (ADR-025 decisions 3, 4's node half, 5, and
// 7) are Track J seam J3, not this package. This package only generates,
// stores, signs with, and exposes the public half of the coordinator's
// own key.
package signingkey
