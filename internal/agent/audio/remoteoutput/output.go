package remoteoutput

// RemoteOutput is the full AUDIO-ENGINE section 8.1 adapter: both
// advance provisioning and logical playout on one destination. Code that
// must not provision (anything reacting to a playback command) should be
// handed only the [PlayoutOutput] half of a value satisfying this
// interface, never the whole thing — see this package's doc comment.
type RemoteOutput interface {
	Provisioner
	PlayoutOutput
}
