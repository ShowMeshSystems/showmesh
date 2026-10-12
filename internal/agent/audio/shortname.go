package audio

import "strings"

// interfaceVendors are maker names dropped from the front of a description,
// longest first so "Native Instruments" wins over a shorter match.
var interfaceVendors = []string{
	"Native Instruments", "Universal Audio", "Allen & Heath", "Solid State Logic",
	"Focusrite", "Behringer", "PreSonus", "Steinberg", "Audient", "M-Audio",
	"Tascam", "Mackie", "Roland", "Yamaha", "Arturia", "MOTU", "Zoom", "RME", "SSL",
}

// interfaceNameNoise are trailing words that describe the connection or the
// PipeWire profile, not the interface.
var interfaceNameNoise = []string{
	"Pro Audio", "USB Audio", "Analog Stereo", "Digital Stereo", "Analog Surround",
	"Analog", "Digital", "Stereo", "Mono", "USB",
}

// ShortInterfaceName is the name an operator reads for an audio interface:
// PipeWire's node.nick when set, else description cut at its first comma or
// parenthesis with the maker and trailing connection words removed.
func ShortInterfaceName(nick, description string) string {
	if nick = strings.TrimSpace(nick); nick != "" {
		return nick
	}
	name := description
	if cut := strings.IndexAny(name, ",("); cut >= 0 {
		name = name[:cut]
	}
	name = strings.TrimSpace(name)
	full := name
	for trimmed := true; trimmed; {
		trimmed = false
		for _, noise := range interfaceNameNoise {
			if rest, ok := cutSuffixWord(name, noise); ok {
				name, trimmed = rest, true
			}
		}
	}
	for _, vendor := range interfaceVendors {
		if rest, ok := cutPrefixWord(name, vendor); ok {
			name = rest
			break
		}
	}
	if name == "" {
		return full
	}
	return name
}

// alsaShortName is the card of an ALSA route, "M4" for "hw:CARD=M4,DEV=0",
// or empty when the route names its card only by number.
func alsaShortName(device string) string {
	if !strings.Contains(device, "CARD=") {
		return ""
	}
	return alsaCardKey(device)
}

func cutSuffixWord(s, word string) (string, bool) {
	if len(s) <= len(word) || !strings.EqualFold(s[len(s)-len(word):], word) || s[len(s)-len(word)-1] != ' ' {
		return s, false
	}
	return strings.TrimSpace(s[:len(s)-len(word)]), true
}

func cutPrefixWord(s, word string) (string, bool) {
	if len(s) <= len(word) || !strings.EqualFold(s[:len(word)], word) || s[len(word)] != ' ' {
		return s, false
	}
	return strings.TrimSpace(s[len(word):]), true
}
