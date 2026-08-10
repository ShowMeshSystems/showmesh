package capability

import "testing"

func TestIDValidate(t *testing.T) {
	tests := []struct {
		name    string
		id      ID
		wantErr bool
	}{
		{name: "known two-segment ID", id: "matrix.render", wantErr: false},
		{name: "known three-segment ID", id: "transport.ndi.send", wantErr: false},
		{name: "unknown but well-formed ID", id: "widget.frobnicate", wantErr: false},
		{name: "withdrawn ID is syntactically valid", id: "audio.playback", wantErr: false},
		{name: "digits in a segment", id: "audio.output.ltc2", wantErr: false},
		{name: "empty string", id: "", wantErr: true},
		{name: "single segment, no dot", id: "matrix", wantErr: true},
		{name: "uppercase", id: "Matrix.Render", wantErr: true},
		{name: "leading dot", id: ".matrix.render", wantErr: true},
		{name: "trailing dot", id: "matrix.render.", wantErr: true},
		{name: "doubled dot", id: "matrix..render", wantErr: true},
		{name: "segment starting with a digit", id: "1matrix.render", wantErr: true},
		{name: "underscore not allowed", id: "matrix.render_hd", wantErr: true},
		{name: "MQTT plus wildcard", id: "matrix.+", wantErr: true},
		{name: "MQTT hash wildcard", id: "matrix.#", wantErr: true},
		{name: "topic-level separator", id: "matrix/render", wantErr: true},
		{name: "dollar sign", id: "$matrix.render", wantErr: true},
		{name: "whitespace", id: "matrix. render", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.id.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestIDIsKnown(t *testing.T) {
	tests := []struct {
		id   ID
		want bool
	}{
		{id: "matrix.render", want: true},
		{id: "video.playback", want: true},
		{id: "media.cache", want: true},
		{id: "display.hdmi", want: true},
		{id: "transport.ndi.send", want: true},
		{id: "transport.ndi.receive", want: true},
		{id: "audio.engine", want: true},
		{id: "audio.output.local", want: true},
		{id: "audio.output.fm", want: true},
		{id: "audio.output.ltc", want: true},
		{id: "audio.output.dante", want: true},
		{id: "timecode.ltc.observe", want: true},
		{id: "process.supervise", want: true},
		{id: "widget.frobnicate", want: false},
		{id: "audio.playback", want: false}, // withdrawn, not known
	}

	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			if got := tt.id.IsKnown(); got != tt.want {
				t.Fatalf("IsKnown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIDIsWithdrawn(t *testing.T) {
	tests := []struct {
		id   ID
		want bool
	}{
		{id: "audio.playback", want: true},
		{id: "audio.multichannel", want: true},
		{id: "audio.dante", want: true},
		{id: "timecode.ltc.generate", want: true},
		{id: "matrix.render", want: false},
		{id: "timecode.ltc.observe", want: false},
		{id: "widget.frobnicate", want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			if got := tt.id.IsWithdrawn(); got != tt.want {
				t.Fatalf("IsWithdrawn() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWithdrawnIDsAreNotHardRejected proves the model layer accepts a
// withdrawn ID rather than rejecting it: the diagnostic belongs to the
// caller (e.g. the agent wiring code in a later step), per the spec.
func TestWithdrawnIDsAreNotHardRejected(t *testing.T) {
	c := Capability{ID: "audio.playback", Version: 1}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil: a withdrawn ID must not be hard-rejected by the model", err)
	}
	if !c.ID.IsWithdrawn() {
		t.Fatalf("IsWithdrawn() = false, want true")
	}
}
