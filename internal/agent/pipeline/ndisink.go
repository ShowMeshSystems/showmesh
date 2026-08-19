package pipeline

// NDISinkStage builds the "sink" [Stage] for a surface whose
// show.surface.output.transport is "ndi": an ndisink element named from
// sourceName. Building and even running this stage's argv successfully
// says nothing about whether a frame can actually reach the network — see
// [ProbeNDISend]'s doc comment. Only a real state transition, either this
// surface's own supervised pipeline reaching PLAYING or a standalone probe,
// is evidence of that.
//
// The videoconvert is required, not defensive: ndisink's sink template
// advertises only UYVY, I420, NV12, NV21, YV12, BGRA, BGRx, RGBA and RGBx,
// none of which is the packed 24-bit RGB an rgb FSEQ surface produces, so
// without it the pipeline fails to negotiate (MEASURED on GStreamer 1.26.2:
// "streaming stopped, reason not-negotiated (-4)"). It lives here rather
// than in [FSEQSourceSpec] so the fakesink diagnostic path stays a
// zero-conversion copy and the conversion's cost is attributable to the
// transport that requires it. ADR-040 decision 2 keeps this conversion in
// GStreamer: ShowMesh may locate, decompress and copy bytes, never convert
// them itself.
func NDISinkStage(sourceName string) Stage {
	return Stage{
		Label: "sink",
		Elements: []Element{
			{Factory: "videoconvert", Name: "sink-convert"},
			{
				Factory: "ndisink",
				Name:    "sink",
				Properties: []Property{
					{Key: "ndi-name", Value: sourceName},
					{Key: "sync", Value: "false"},
				},
			},
		},
	}
}
