package config

import "testing"

func decodeSequenceTest(raw string, claims map[string][]string) (SequenceTestPayload, *ValidationError) {
	showExists := func(id string) bool { return id == "show-1" }
	resolveCue := func(id string) (string, bool) {
		switch id {
		case "intro", "thriller":
			return "show-1", true
		case "other-show-cue":
			return "show-2", true
		}
		return "", false
	}
	return DecodeSequenceTestPayload(raw, "show-1", showExists, resolveCue, claims)
}

func TestSequenceTestRefusesAFilenameAPlaylistPlaysForAnotherCue(t *testing.T) {
	_, verr := decodeSequenceTest(`{"entries":[{"sequenceFilename":"Intro.fseq","cue":"thriller"}]}`, map[string][]string{"Intro.fseq": {"intro"}})
	if verr == nil || verr.Code != ValidationCodeSequenceTestFilenameClaimed {
		t.Fatalf("verr = %+v, want %s", verr, ValidationCodeSequenceTestFilenameClaimed)
	}
}

func TestSequenceTestRefusesOneFilenameForTwoCues(t *testing.T) {
	_, verr := decodeSequenceTest(`{"entries":[{"sequenceFilename":"A.fseq","cue":"intro"},{"sequenceFilename":"A.fseq","cue":"thriller"}]}`, nil)
	if verr == nil || verr.Code != ValidationCodeSequenceTestFilenameClaimed {
		t.Fatalf("verr = %+v, want %s", verr, ValidationCodeSequenceTestFilenameClaimed)
	}
}

func TestSequenceTestAcceptsTheSameCueTwice(t *testing.T) {
	p, verr := decodeSequenceTest(`{"show":"show-1","entries":[{"sequenceFilename":"Intro.fseq","cue":"intro"},{"sequenceFilename":"Intro2.fseq","cue":"intro"},{"sequenceFilename":"Intro.fseq","cue":"intro"}]}`, map[string][]string{"Intro.fseq": {"intro"}})
	if verr != nil {
		t.Fatalf("verr = %+v, want accepted", verr)
	}
	if len(p.Entries) != 3 {
		t.Fatalf("entries = %+v", p.Entries)
	}
}

func TestSequenceTestRefusesACueOfAnotherShow(t *testing.T) {
	_, verr := decodeSequenceTest(`{"entries":[{"sequenceFilename":"X.fseq","cue":"other-show-cue"}]}`, nil)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("verr = %+v, want %s", verr, ValidationCodeFieldUnknownReference)
	}
}

func TestSequenceTestRefusesAShowMismatch(t *testing.T) {
	if _, verr := decodeSequenceTest(`{"show":"show-2","entries":[]}`, nil); verr == nil {
		t.Fatal("want a refusal when the body names a different show than the address")
	}
}
