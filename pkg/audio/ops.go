package audio

// Operation is one of the fifteen reserved agent operations, in the
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
)

var operations = map[string]struct{}{
	string(OperationSessionApply): {}, string(OperationSessionPrepare): {}, string(OperationSessionStart): {},
	string(OperationSessionPause): {}, string(OperationSessionResume): {}, string(OperationSessionSeek): {},
	string(OperationSessionAdvance): {}, string(OperationSessionStop): {}, string(OperationSessionClear): {},
	string(OperationGainSet): {}, string(OperationGainFade): {}, string(OperationOutputMute): {},
	string(OperationOutputUnmute): {}, string(OperationDeviceProbe): {}, string(OperationMediaProbe): {},
}

// Validate reports whether op is one of the fifteen reserved operations.
func (op Operation) Validate() error {
	return closedSet("audio.Operation", string(op), operations)
}

// Operations returns the fifteen reserved operations in the declaration
// order above. The returned slice is a fresh copy on every call.
func Operations() []Operation {
	return []Operation{
		OperationSessionApply, OperationSessionPrepare, OperationSessionStart,
		OperationSessionPause, OperationSessionResume, OperationSessionSeek,
		OperationSessionAdvance, OperationSessionStop, OperationSessionClear,
		OperationGainSet, OperationGainFade, OperationOutputMute,
		OperationOutputUnmute, OperationDeviceProbe, OperationMediaProbe,
	}
}
