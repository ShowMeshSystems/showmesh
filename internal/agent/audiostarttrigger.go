package agent

import (
	"sync"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// audioStartTriggerRecord is one session's most recent start-trigger
// evidence (ADR-051 decision 6): how it was actually started, and, for
// the "already started by MultiSync" check activateAudio runs before it
// would otherwise restart or reseek (item 7 of that decision), which Cue
// and media that start belongs to. CueID and MediaIdentity live here,
// never on [audio.Session] itself, for the identical reason
// cueActivationOperation already tracks announcementCueID outside
// package audio (cueactivationops.go): a session carries no Cue identity
// of its own to read back.
type audioStartTriggerRecord struct {
	Trigger          string
	CueID            string
	MediaIdentity    string
	SequenceFilename string
	ArrivalNs        int64
	LeadMs           int64
	PreparedLate     bool
}

// cueActivationTriggerRegistry is this node's ONE shared start-trigger
// evidence registry (ADR-051 decision 6), read and written by both
// activateAudio (internal/agent/cueactivationaudio.go, the "coordinator"
// writer and item 7's restart-guard reader) and the MultiSync cue-audio
// trigger (multisynccueaudio.go, the "multisync" writer) without either
// one needing a reference to the other or to agent.go's own wiring,
// matching audiocapabilities.go's audioEngineAvailable/audioEngineHeldNode
// package-level "one per node process" convention (ADR-026 N=1 applied to
// this evidence the way it already applies to the audio engine itself).
// nil until agent.go's Run sets it, exactly like those two vars; every
// reader here already treats that as "no evidence recorded yet," never a
// crash.
var cueActivationTriggerRegistry *audioStartTriggerRegistry

// audioStartTriggerRegistry holds the most recent audioStartTriggerRecord
// per session id, shared between the MultiSync cue-audio trigger
// (multisynccueaudio.go, the writer for a "multisync" start), activateAudio
// (internal/agent/cueactivationaudio.go, the writer for a "coordinator"
// start and the reader for item 7's restart guard), and the audio report
// loop (audioreport.go, a reader). A nil *audioStartTriggerRegistry is
// never constructed by this package; every caller here is handed a real
// one from agent.go, matching this package's other shared-state
// constructors.
type audioStartTriggerRegistry struct {
	mu      sync.Mutex
	records map[pkgaudio.SessionID]audioStartTriggerRecord
}

func newAudioStartTriggerRegistry() *audioStartTriggerRegistry {
	return &audioStartTriggerRegistry{records: make(map[pkgaudio.SessionID]audioStartTriggerRecord)}
}

// set replaces id's start-trigger record.
func (r *audioStartTriggerRegistry) set(id pkgaudio.SessionID, rec audioStartTriggerRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[id] = rec
}

// get returns id's start-trigger record, or (zero, false) if this session
// has never started since the registry (and therefore the agent process)
// came up.
func (r *audioStartTriggerRegistry) get(id pkgaudio.SessionID) (audioStartTriggerRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	return rec, ok
}
