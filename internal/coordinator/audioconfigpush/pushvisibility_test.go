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

// TestReachableCeilingBelowMinimumSilentlyStrandsEveryNode is this
// issue's own reproduction: a legal pre-decibel linear ceiling of 0.0005 migrates
// through schemaV19 (which clamps only the upper bound) to -66.02 dB,
// below the minimum bound DecodeAudioSettingsPayload enforces today
// (audiosettings.go's minDefaultMaxBackgroundGainDb, -60 dB). The stored
// revision no longer decodes, and on the unmodified tree the only
// consequence anywhere is a single Warn log line: no
// audio.settings.configure reaches the node, and nothing else says so.
func TestReachableCeilingBelowMinimumSilentlyStrandsEveryNode(t *testing.T) {
	dir := writePreDecibelDatabase(t, 0.0005, 0.25)

	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("reopen and migrate: %v", err)
	}
	defer func() { _ = st.Close() }()

	pub := &fakePublisher{}
	pushErr := ToNode(context.Background(), st, pub, time.Now, "node-a")
	if pushErr == nil {
		t.Fatal("ToNode succeeded: the migrated ceiling was expected to be below the minimum bound and refuse to decode")
	}
	if !strings.Contains(pushErr.Error(), "defaultMaxBackgroundGainDb") {
		t.Fatalf("ToNode error = %v, want it to name defaultMaxBackgroundGainDb", pushErr)
	}
	if _, published := pub.actionParams("audio.settings.configure"); published {
		t.Fatal("audio.settings.configure was published despite the decode failure")
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	BestEffort(context.Background(), st, pub, time.Now, "node-a", logger)
	if !strings.Contains(logBuf.String(), "audio config push failed") {
		t.Fatalf("expected the Warn log line, got: %s", logBuf.String())
	}
	t.Logf("observed on unmodified tree: ToNode error = %q; BestEffort's only trace is the Warn log: %s", pushErr, logBuf.String())
}
