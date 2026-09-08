package audio

// Operation is one of the eighteen reserved agent operations, in the
// exact spelling the identifier register carries. select_media,
// select_playlist, set_loop, announce, and duck
// from AUDIO-ENGINE section 14 mint no operation of their own: they are
// properties of the session an OperationSessionApply applies (an
// announcement is OperationSessionApply with SourceRoleAnnouncement and a
// declared MixPolicy, then OperationSessionStart).
type Operation string

const (
	OperationSessionApply   Operation = "audio.session.apply"
	OperationSessionPrepare Operation = "audio.session.prepare"
	OperationSessionStart   Operation = "audio.session.start"
	OperationSessionPause   Operation = "audio.session.pause"
	OperationSessionResume  Operation = "audio.session.resume"
	OperationSessionSeek    Operation = "audio.session.seek"
	OperationSessionAdvance Operation = "audio.session.advance"
	OperationSessionStop    Operation = "audio.session.stop"
	OperationSessionClear   Operation = "audio.session.clear"
	OperationGainSet        Operation = "audio.gain.set"
	OperationGainFade       Operation = "audio.gain.fade"
	OperationOutputMute     Operation = "audio.output.mute"
	OperationOutputUnmute   Operation = "audio.output.unmute"
	OperationDeviceProbe    Operation = "audio.device.probe"
	OperationMediaProbe     Operation = "audio.media.probe"

	// OperationNodeConfigure and OperationSettingsConfigure are the
	// coordinator's ADR-039 configuration push, never an operator-issued
	// command: the coordinator sends them on every audio.node/
	// audio.settings config write and on every node hello, so an agent
	// converges on the current revision without a restart.
	OperationNodeConfigure     Operation = "audio.node.configure"
	OperationSettingsConfigure Operation = "audio.settings.configure"

	// OperationNodeSilence is node-scoped like OperationNodeConfigure: no
	// sessionId, no revision. It stops every session this node's Manager
	// currently holds unconditionally, so an installation-wide emergency
	// stop can reach a session the coordinator never observed.
	OperationNodeSilence Operation = "audio.node.silence"
)

var operations = map[string]struct{}{
	string(OperationSessionApply): {}, string(OperationSessionPrepare): {}, string(OperationSessionStart): {},
	string(OperationSessionPause): {}, string(OperationSessionResume): {}, string(OperationSessionSeek): {},
	string(OperationSessionAdvance): {}, string(OperationSessionStop): {}, string(OperationSessionClear): {},
	string(OperationGainSet): {}, string(OperationGainFade): {}, string(OperationOutputMute): {},
	string(OperationOutputUnmute): {}, string(OperationDeviceProbe): {}, string(OperationMediaProbe): {},
	string(OperationNodeConfigure): {}, string(OperationSettingsConfigure): {},
	string(OperationNodeSilence): {},
}

// Validate reports whether op is one of the eighteen reserved operations.
func (op Operation) Validate() error {
	return closedSet("audio.Operation", string(op), operations)
}

// Operations returns the eighteen reserved operations in the declaration
// order above. The returned slice is a fresh copy on every call.
func Operations() []Operation {
	return []Operation{
		OperationSessionApply, OperationSessionPrepare, OperationSessionStart,
		OperationSessionPause, OperationSessionResume, OperationSessionSeek,
		OperationSessionAdvance, OperationSessionStop, OperationSessionClear,
		OperationGainSet, OperationGainFade, OperationOutputMute,
		OperationOutputUnmute, OperationDeviceProbe, OperationMediaProbe,
		OperationNodeConfigure, OperationSettingsConfigure, OperationNodeSilence,
	}
}
