package fppmqtt

import (
	"testing"
	"time"
)

// TestMessageStoreLatestReceivedAtNeverStoredInstance proves an instance id
// with no put call ever made for it reports everPublished=false and the
// zero Time, never anything a caller could mistake for a real, recent
// receipt: absent bookkeeping must be distinguishable from real evidence.
func TestMessageStoreLatestReceivedAtNeverStoredInstance(t *testing.T) {
	s := newMessageStore()

	got, everPublished := s.latestReceivedAt("never-stored")
	if everPublished {
		t.Fatalf("everPublished = true, want false for an instance id with no message ever put")
	}
	if !got.IsZero() {
		t.Fatalf("latestReceivedAt = %v, want the zero Time for an instance id with no message ever put", got)
	}
}

// TestMessageStoreLatestReceivedAtReportsTheMostRecentAcrossSuffixes proves
// latestReceivedAt looks across every topic suffix for the instance, not
// just one, and picks the most recent.
func TestMessageStoreLatestReceivedAtReportsTheMostRecentAcrossSuffixes(t *testing.T) {
	s := newMessageStore()
	earlier := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(5 * time.Second)

	s.put("main", "status", message{receivedAt: earlier})
	s.put("main", "sensors", message{receivedAt: later})

	got, everPublished := s.latestReceivedAt("main")
	if !everPublished {
		t.Fatalf("everPublished = false, want true after two put calls")
	}
	if !got.Equal(later) {
		t.Fatalf("latestReceivedAt = %v, want the later of the two suffixes' receivedAt (%v)", got, later)
	}
}
