package assetstore

import "time"

// DefaultMaxUploadBytes bounds a single asset upload absent an explicit
// SHOWMESH_ASSET_MAX_UPLOAD_BYTES override.
const DefaultMaxUploadBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB

// MinTransferBytesPerSecond is a labelled ASSUMPTION about the slowest
// link an asset upload or fetch is expected to still complete over. It is
// NOT a measurement of any real network in this project — this project
// forbids claiming verification that has not happened — and exists only
// to derive UploadBudget's deadline.
const MinTransferBytesPerSecond int64 = 1024 * 1024 // 1 MiB/s

// uploadBudgetGrace is added to the size-derived transfer time to absorb
// connection setup and momentary stalls that are not the transfer itself.
const uploadBudgetGrace = 30 * time.Second

// uploadBudgetCeiling caps UploadBudget so a very large or corrupt size
// can never produce an unbounded deadline.
const uploadBudgetCeiling = 2 * time.Hour

// UploadBudget returns how long a transfer of size bytes is allowed to
// take: size / MinTransferBytesPerSecond, plus uploadBudgetGrace, clamped
// to uploadBudgetCeiling. Both the coordinator's upload handler (which
// must extend its own read deadline past httpapi's 10s ReadTimeout,
// internal/coordinator/httpapi/server.go:90) and showmeshctl's HTTP
// client (assets upload/fetch) are meant to compute their timeout from
// this one function, so the two sides of that timeout never drift apart
// the way this project's server/CLI timeout pair once did.
func UploadBudget(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	transferSeconds := float64(size) / float64(MinTransferBytesPerSecond)
	budget := time.Duration(transferSeconds*float64(time.Second)) + uploadBudgetGrace
	if budget > uploadBudgetCeiling {
		return uploadBudgetCeiling
	}
	return budget
}
