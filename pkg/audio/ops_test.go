package audio

import "testing"

func TestOperationsAreExactlyFifteen(t *testing.T) {
	ops := Operations()
	if len(ops) != 15 {
		t.Fatalf("Operations(): got %d entries, want 15", len(ops))
	}
	want := map[Operation]struct{}{
		"audio.session.apply": {}, "audio.session.prepare": {}, "audio.session.start": {},
		"audio.session.pause": {}, "audio.session.resume": {}, "audio.session.seek": {},
		"audio.session.advance": {}, "audio.session.stop": {}, "audio.session.clear": {},
		"audio.gain.set": {}, "audio.gain.fade": {}, "audio.output.mute": {},
		"audio.output.unmute": {}, "audio.device.probe": {}, "audio.media.probe": {},
	}
	seen := make(map[Operation]struct{}, len(ops))
	for _, op := range ops {
		if _, expected := want[op]; !expected {
			t.Errorf("Operations() included unexpected %q", op)
		}
		seen[op] = struct{}{}
	}
	for op := range want {
		if _, ok := seen[op]; !ok {
			t.Errorf("Operations() missing %q", op)
		}
	}
}

func TestOperationValidateRejectsUnknown(t *testing.T) {
	for _, op := range Operations() {
		if err := op.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", op, err)
		}
	}
	if err := Operation("audio.session.teleport").Validate(); err == nil {
		t.Error("Validate(unknown operation) = nil, want error")
	}
}
