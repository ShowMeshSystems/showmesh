package copycheck

import (
	"slices"
	"strings"
	"testing"
)

const ownerFixture = `node "pi-audio-01" deployed with operator override: Cues "bg1" and "kpop-audio" both use the program audio output on pi-audio-01; Cues "bg1" and "wake-up" both use the program audio output on pi-audio-01 (accepted by admin at revision "fd39e194debf3e4c49ef16eae8314ae83e1744b5481e76216b8d1f93249cda77")`

func rulesOf(message string) []string {
	var rules []string
	for _, v := range Check(message) {
		rules = append(rules, v.Rule)
	}
	return rules
}

func TestCheckRules(t *testing.T) {
	cases := []struct {
		rule string
		pass []string
		fail []string
	}{
		{RuleCapital,
			[]string{"Cue bg1 is playing.", "3 nodes are offline."},
			[]string{"cue bg1 is playing.", `"bg1" is playing.`}},
		{RuleFullStop,
			[]string{"Cue bg1 is playing.", "Is the node on?", "Stop!"},
			[]string{"Cue bg1 is playing", "Cue bg1 is playing:"}},
		{RuleSentences,
			[]string{"The node is offline. Check its power.", "Gain is 1.5 dB."},
			[]string{"The node is offline. Check its power. Then start again.", "One. Two. Three"}},
		{RuleLength,
			[]string{strings.Repeat("a", 159) + "."},
			[]string{strings.Repeat("a", 160) + "."}},
		{RuleJoined,
			[]string{"The node is offline: check its power."},
			[]string{"Cue bg1 failed; Cue bg2 failed.", "Cue bg1 failed.; next.", "Cue bg1 failed..", "Start failed: node pi: timed out."}},
		{RuleHex,
			[]string{"Version fd39e194 is live.", "Cue deadbeef is playing."},
			[]string{"Version fd39e194debf3e4c is live.", "Run 3f2b8c1e-9a4d-4c1b-8f0e-2d6a7b9c0e1f failed."}},
		{RuleGoError,
			[]string{"The node did not answer in time.", "The file is missing."},
			[]string{
				"Bad value %!s(MISSING).", "api: the request failed.", "Start failed: context deadline exceeded.",
				"open /x: no such file or directory.", "The FPP said connection refused.", "Saw invalid character in the body.",
				"Got unexpected end of JSON input.", "Read failed with EOF.", "Body is wrong, json: cannot read it.", "Could not dial tcp to the node.",
			}},
		{RuleVocabulary,
			[]string{"The node is prevented from starting.", "Every node is ready."},
			[]string{"The interlock is open.", "Accepted at Revision 4.", "This rev is old.", "The command was dispatched.", "Could not decode the answer."}},
		{RuleKindName,
			[]string{"Cue bg1 is playing.", "File intro.mp4 is missing.", "Gain is 1.5 dB.", "Node pi.local is offline."},
			[]string{"The show.cue named bg1 is missing.", "No audio.node is set.", "The night.session is closed.", "Lost audio.session.state from the node."}},
		{RuleQuotedName,
			[]string{"Cue bg1 is playing."},
			[]string{`Cue "bg1" is playing.`}},
	}
	for _, c := range cases {
		for _, message := range c.pass {
			if slices.Contains(rulesOf(message), c.rule) {
				t.Errorf("%s: %q should pass", c.rule, message)
			}
		}
		for _, message := range c.fail {
			if !slices.Contains(rulesOf(message), c.rule) {
				t.Errorf("%s: %q should fail", c.rule, message)
			}
		}
	}
}

func TestCheckOwnerFixtureFails(t *testing.T) {
	got := rulesOf(ownerFixture)
	want := []string{RuleCapital, RuleFullStop, RuleLength, RuleJoined, RuleHex, RuleVocabulary, RuleQuotedName}
	if !slices.Equal(got, want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
}

func TestCheckAcceptsAGoodMessage(t *testing.T) {
	for _, message := range []string{"", "Cue bg1 is not on node pi-audio-01. Deploy it and try again."} {
		if got := Check(message); len(got) != 0 {
			t.Errorf("Check(%q) = %v, want none", message, got)
		}
	}
}

func TestBannedWordsComeFromTheFile(t *testing.T) {
	if !slices.Contains(BannedWords, "interlock") || slices.ContainsFunc(BannedWords, func(w string) bool { return strings.ContainsAny(w, "# ") }) {
		t.Fatalf("BannedWords = %v", BannedWords)
	}
}
