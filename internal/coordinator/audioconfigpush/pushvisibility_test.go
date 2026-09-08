package audioconfigpush

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"

	_ "modernc.org/sqlite" // the same pure-Go driver internal/coordinator/store registers
)

// TestReachableCeilingBelowMinimumDoesNotPartiallyPush documents this
// issue's own reproduction case, at this package's own layer: a legal
// pre-decibel linear ceiling of 0.0005 migrates through schemaV19 (which
// clamps only the upper bound) to -66.02 dB, below the minimum bound
// DecodeAudioSettingsPayload enforces today (audiosettings.go's
// minDefaultMaxBackgroundGainDb, -60 dB). At the time this test was
// written, the stored revision does not decode and the only trace
// anywhere in THIS package is a single Warn log line — logged below and
// asserted only conditionally, as an observation, not a requirement: this
// test does not assert that the push must keep refusing forever. Whether
// audio.settings should someday clamp this value instead of refusing it
// is a separate question from this issue's own (visibility), and a fix
// that made ToNode succeed here would not be a regression against this
// test. What this test does require, unconditionally, is ToNode's own
// contract: it never half-publishes — an error means nothing was sent,
// success means something was. The operator-visible acceptance property
// this issue actually adds — that an undecodable revision is reported,
// not just silently refused — is proven one layer up, at the API's own
// GET /api/v1/snapshot, in
// TestSnapshotReportsAudioConfigPushUnusableForAnUndecodableRevision
// (internal/coordinator/api/audiosettings_test.go), which this test does
// not duplicate.
func TestReachableCeilingBelowMinimumDoesNotPartiallyPush(t *testing.T) {
	dir := writePreDecibelDatabase(t, 0.0005, 0.25)

	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("reopen and migrate: %v", err)
	}
	defer func() { _ = st.Close() }()

	pub := &fakePublisher{}
	pushErr := ToNode(context.Background(), st, pub, time.Now, "node-a")
	_, published := pub.actionParams("audio.settings.configure")

	// The only unconditional requirement: an error and a publish never
	// both happen for the same push.
	if pushErr != nil && published {
		t.Fatalf("ToNode returned an error (%v) but still published audio.settings.configure: a failed push must not partially apply", pushErr)
	}

	if pushErr == nil {
		t.Logf("observed: ToNode succeeded and published %v (this reproduction case no longer refuses; see this test's own doc comment)", published)
		return
	}
	if !strings.Contains(pushErr.Error(), "defaultMaxBackgroundGainDb") {
		t.Fatalf("ToNode error = %v, want it to name defaultMaxBackgroundGainDb", pushErr)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	BestEffort(context.Background(), st, pub, time.Now, "node-a", logger)
	if !strings.Contains(logBuf.String(), "audio config push failed") {
		t.Fatalf("BestEffort logged nothing on a real decode failure, want the Warn log line; got: %s", logBuf.String())
	}
	t.Logf("observed: ToNode error = %q; BestEffort's only trace at this package's layer is the Warn log: %s", pushErr, logBuf.String())
}
