package store

import (
	"context"
	"testing"
	"time"
)

// BenchmarkProbeAuditWrite measures the marginal cost identity.svc.AuditWriteStatus
// adds to every GET /api/v1/snapshot request: a real INSERT into audit_log
// inside a transaction, always rolled back. Answers the question the
// finding-5 redesign (a per-request probe replacing a passive latch) must
// justify on a polled path.
func BenchmarkProbeAuditWrite(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.ProbeAuditWrite(ctx); err != nil {
			b.Fatalf("ProbeAuditWrite: %v", err)
		}
	}
}
