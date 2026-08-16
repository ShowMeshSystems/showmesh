// Package assetstore is the coordinator's content-addressed blob store for
// show assets (ADR-028): FSEQ, audio, and other media files distributed to
// nodes. It stores and retrieves bytes by their sha256 content hash through
// the [Backend] interface. Metadata — show, sequence, target, provenance —
// lives in SQLite (internal/coordinator/store); this package never imports
// that one, and never holds a byte in a database (ADR-028 decision 4).
//
// The only shipped [Backend] is [VolumeBackend], a plain directory on the
// coordinator's own filesystem or a mounted volume. SMB/NAS is deployment
// configuration for a future backend behind the same interface, not
// something this package builds ahead of a deployment that needs it
// (ADR-028 decision 4).
//
// Every write goes stage, hash, rename: bytes land in a per-upload staging
// file, are hashed while they are written, and are renamed into their
// final content-addressed path only once the whole stream has been read
// without error. No code path renames a partially read stream, so an
// interrupted upload registers nothing (ADR-030), and a crash mid-upload
// leaves at most an orphaned staging file, swept on the next
// [NewVolumeBackend] call.
//
// budget.go holds the one size/timeout budget this package derives:
// [DefaultMaxUploadBytes], the [MinTransferBytesPerSecond] assumption, and
// [UploadBudget]. The coordinator's upload handler and showmeshctl's
// upload/fetch client are both meant to compute their deadlines from
// [UploadBudget] rather than each picking their own number.
package assetstore
