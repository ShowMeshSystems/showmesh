package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file tests runAssetInventory against fakePublisher (see
// fake_publisher_test.go) — no broker involved, matching heartbeat_test.go's
// established style and reusing its fakeClock and sendAndAwait helpers.

func decodeAssetInventory(t *testing.T, payload []byte) mqttproto.AssetInventoryPayload {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	inv, err := mqttproto.DecodeAssetInventoryPayload(env)
	if err != nil {
		t.Fatalf("DecodeAssetInventoryPayload() error = %v", err)
	}
	return inv
}

// TestRunAssetInventoryPublishesOnTick proves a tick produces a retained
// publish on the node's observed/assets topic reflecting the asset
// directory's real contents.
func TestRunAssetInventoryPublishesOnTick(t *testing.T) {
	dir := t.TempDir()
	content := []byte("show asset bytes")
	if err := os.WriteFile(filepath.Join(dir, "show.fseq"), content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAssetInventory(ctx, pub, "media-03", dir, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runAssetInventory did not return after ctx cancellation")
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}

	wantTopic, err := mqttproto.ObservedTopic("media-03", "assets")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}
	if calls[0].topic != wantTopic {
		t.Fatalf("topic = %q, want %q", calls[0].topic, wantTopic)
	}
	if !calls[0].retain {
		t.Fatalf("retain = false, want true (ObservedDeliveryPolicy is retained)")
	}
	if calls[0].qos != mqttproto.ObservedDeliveryPolicy.QoS {
		t.Fatalf("qos = %d, want %d", calls[0].qos, mqttproto.ObservedDeliveryPolicy.QoS)
	}

	inv := decodeAssetInventory(t, calls[0].payload)
	if !inv.Complete {
		t.Fatalf("Complete = false (%q), want true for a fully-readable directory", inv.Reason)
	}
	if len(inv.Assets) != 1 {
		t.Fatalf("len(Assets) = %d, want 1", len(inv.Assets))
	}
	if inv.Assets[0].Filename != "show.fseq" {
		t.Fatalf("Filename = %q, want %q", inv.Assets[0].Filename, "show.fseq")
	}
}

// TestRunAssetInventoryMissingDirectoryReportsIncomplete proves the
// published payload itself carries complete=false with a reason when the
// asset directory does not exist — this is the wire-level proof that the
// "complete has to be earned" rule actually reaches the coordinator, not
// just enumerateAssets' own return values (already covered in
// assets_test.go).
func TestRunAssetInventoryMissingDirectoryReportsIncomplete(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAssetInventory(ctx, pub, "media-03", missing, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	inv := decodeAssetInventory(t, calls[0].payload)
	if inv.Complete {
		t.Fatalf("Complete = true, want false for a missing asset directory")
	}
	if inv.Reason == "" {
		t.Fatalf("Reason is empty, want an explanation")
	}
}

// TestRunAssetInventoryTriggerPublishesImmediately proves a signal on the
// triggered channel (what command.go sends after a completed asset.fetch)
// produces an immediate publish without waiting for a tick — the mechanism
// that lets a sync's confirmation rest on a post-dispatch inventory report.
func TestRunAssetInventoryTriggerPublishesImmediately(t *testing.T) {
	dir := t.TempDir()
	pub := newFakePublisher()
	ticks := make(chan time.Time)
	triggered := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAssetInventory(ctx, pub, "media-03", dir, time.Now, ticks, triggered, discardLogger())
	}()

	select {
	case triggered <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending trigger")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the triggered publish")
	}

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1 (a trigger with no tick having fired yet)", len(calls))
	}
}

// TestRunAssetInventoryPublishFailureDoesNotStopLaterTicks mirrors
// heartbeat.go's TestRunHeartbeatPublishFailureDoesNotStopLaterTicks: a
// transient publish failure on one tick must not wedge the loop.
func TestRunAssetInventoryPublishFailureDoesNotStopLaterTicks(t *testing.T) {
	dir := t.TempDir()
	pub := newFakePublisher()
	pub.failOn = map[int]bool{0: true} // the first tick's publish fails

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAssetInventory(ctx, pub, "media-03", dir, time.Now, ticks, nil, discardLogger())
	}()

	for i := 0; i < 2; i++ {
		select {
		case ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out sending tick %d", i)
		}
		select {
		case <-pub.notify:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for publish notification for tick %d", i)
		}
	}

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2 (a failed publish attempt must still be followed by the next tick)", len(calls))
	}
}

// TestRunAssetInventoryReturnsOnContextDone mirrors
// TestRunHeartbeatReturnsOnContextDone.
func TestRunAssetInventoryReturnsOnContextDone(t *testing.T) {
	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAssetInventory(ctx, pub, "media-03", t.TempDir(), time.Now, ticks, nil, discardLogger())
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runAssetInventory did not return promptly after ctx cancellation")
	}
}

// TestRunAssetInventoryCachesAcrossTicks proves the hash cache used across
// ticks is the SAME map instance for the whole loop lifetime (not rebuilt
// per tick): a file is written once, hashed on the first tick, then its
// bytes are corrupted without touching size/modTime, and the second tick's
// publish must still report the ORIGINAL (cached) hash — this is
// runAssetInventory's own integration of assets_test.go's
// TestEnumerateAssetsCachesUnchangedFiles, proven at the publish-loop level.
func TestRunAssetInventoryCachesAcrossTicks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "show.fseq")
	original := []byte("original bytes, hash gets cached across ticks")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	originalModTime := info.ModTime()

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runAssetInventory(ctx, pub, "media-03", dir, time.Now, ticks, nil, discardLogger())
	}()

	sendTick := func() {
		select {
		case ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("timed out sending tick")
		}
		select {
		case <-pub.notify:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for publish")
		}
	}

	sendTick()

	corrupted := append([]byte(nil), original...)
	corrupted[0] ^= 0xFF
	if err := os.WriteFile(path, corrupted, 0o644); err != nil {
		t.Fatalf("WriteFile(corrupted): %v", err)
	}
	if err := os.Chtimes(path, originalModTime, originalModTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	sendTick()

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}
	first := decodeAssetInventory(t, calls[0].payload)
	second := decodeAssetInventory(t, calls[1].payload)
	if len(first.Assets) != 1 || len(second.Assets) != 1 {
		t.Fatalf("expected exactly one asset in each report: first=%+v second=%+v", first, second)
	}
	if second.Assets[0].ContentHash != first.Assets[0].ContentHash {
		t.Fatalf("ContentHash changed across ticks with an unchanged (size, modTime): first=%q second=%q — the cache was not reused across the loop's lifetime",
			first.Assets[0].ContentHash, second.Assets[0].ContentHash)
	}
}
