package pipeline

// NDISinkStage builds the "sink" [Stage] for a surface whose
// show.surface.output.transport is "ndi": an ndisink element named from
// sourceName. Building and even running this stage's argv successfully
// says nothing about whether a frame can actually reach the network — see
// [ProbeNDISend]'s doc comment. Only a real state transition, either this
// surface's own supervised pipeline reaching PLAYING or a standalone probe,
// is evidence of that.
func NDISinkStage(sourceName string) Stage {
	return Stage{
		Label: "sink",
		Elements: []Element{{
			Factory: "ndisink",
			Name:    "sink",
			Properties: []Property{
				{Key: "ndi-name", Value: sourceName},
				{Key: "sync", Value: "false"},
			},
		}},
	}
}
