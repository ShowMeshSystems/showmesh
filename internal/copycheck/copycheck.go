// Package copycheck judges a message a person finally reads against the
// operator copy standard. It depends on nothing else in this repository.
package copycheck

import (
	_ "embed"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule names. They are keys in the checked-in lists, so they never change.
const (
	RuleCapital    = "capital"
	RuleFullStop   = "full-stop"
	RuleSentences  = "sentences"
	RuleLength     = "length"
	RuleJoined     = "joined"
	RuleHex        = "hex"
	RuleGoError    = "go-error"
	RuleVocabulary = "vocabulary"
	RuleKindName   = "kind-name"
	RuleQuotedName = "quoted-name"
)

const (
	maxLength    = 160
	maxSentences = 2
)

// Violation is one rule a message breaks. Match is the offending fragment
// when the rule has one.
type Violation struct {
	Rule  string
	Match string
}

//go:embed banned-words.txt
var bannedWordsFile string

// BannedWords is the vocabulary list, lowercase, in file order.
var BannedWords = parseWords(bannedWordsFile)

func parseWords(file string) []string {
	var words []string
	for _, line := range strings.Split(file, "\n") {
		line, _, _ = strings.Cut(line, "#")
		if word := strings.ToLower(strings.TrimSpace(line)); word != "" {
			words = append(words, word)
		}
	}
	return words
}

var (
	vocabularyPattern = regexp.MustCompile(`(?i)\b(` + strings.Join(quoteAll(BannedWords), "|") + `)\b`)
	sentenceEnd       = regexp.MustCompile(`[.?!]+(\s+|$)`)
	hexPattern        = regexp.MustCompile(`[0-9a-fA-F]{16,}|\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	packagePrefix     = regexp.MustCompile(`^[a-z][a-z0-9_./-]*: `)
	wholeEOF          = regexp.MustCompile(`\bEOF\b`)
	dottedName        = regexp.MustCompile(`\b[a-z][a-z0-9]+(\.[a-z][a-z0-9_-]+)+\b`)
	quotedName        = regexp.MustCompile(`"[^"]+"`)
)

var goErrorMarks = []string{
	"%!",
	"context deadline exceeded",
	"no such file or directory",
	"connection refused",
	"invalid character",
	"unexpected end of JSON",
	"json:",
	"dial tcp",
}

// A dotted lowercase word ending in one of these is a file or host name,
// not an object kind or a signal name.
var fileAndHostSuffixes = map[string]bool{
	"mp4": true, "mov": true, "mkv": true, "wav": true, "mp3": true, "flac": true, "ogg": true,
	"fseq": true, "json": true, "yaml": true, "yml": true, "txt": true, "log": true, "png": true,
	"jpg": true, "gz": true, "tar": true, "zip": true, "deb": true, "sh": true, "service": true,
	"local": true, "lan": true, "com": true, "dev": true, "org": true, "net": true, "io": true,
}

func quoteAll(words []string) []string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = regexp.QuoteMeta(word)
	}
	return quoted
}

// Check returns every rule message breaks, at most one Violation per rule,
// in a fixed order. An empty message breaks none.
func Check(message string) []Violation {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	var found []Violation
	add := func(rule, match string) { found = append(found, Violation{Rule: rule, Match: match}) }

	first, _ := utf8.DecodeRuneInString(message)
	if !unicode.IsUpper(first) && !unicode.IsDigit(first) {
		add(RuleCapital, string(first))
	}
	if !strings.HasSuffix(message, ".") && !strings.HasSuffix(message, "?") && !strings.HasSuffix(message, "!") {
		add(RuleFullStop, "")
	}
	if countSentences(message) > maxSentences {
		add(RuleSentences, "")
	}
	if utf8.RuneCountInString(message) > maxLength {
		add(RuleLength, "")
	}
	if match := joinedMark(message); match != "" {
		add(RuleJoined, match)
	}
	if match := hexPattern.FindString(message); match != "" {
		add(RuleHex, match)
	}
	if match := goErrorMark(message); match != "" {
		add(RuleGoError, match)
	}
	if match := vocabularyPattern.FindString(message); match != "" {
		add(RuleVocabulary, strings.ToLower(match))
	}
	if match := kindName(message); match != "" {
		add(RuleKindName, match)
	}
	if match := quotedName.FindString(message); match != "" {
		add(RuleQuotedName, match)
	}
	return found
}

func countSentences(message string) int {
	ends := sentenceEnd.FindAllStringIndex(message, -1)
	count := len(ends)
	if count == 0 || ends[count-1][1] != len(message) {
		count++
	}
	return count
}

func joinedMark(message string) string {
	for _, mark := range []string{"; ", ".;", ".."} {
		if strings.Contains(message, mark) {
			return mark
		}
	}
	if strings.Count(message, ": ") > 1 {
		return ": "
	}
	return ""
}

func goErrorMark(message string) string {
	for _, mark := range goErrorMarks {
		if strings.Contains(message, mark) {
			return mark
		}
	}
	if match := wholeEOF.FindString(message); match != "" {
		return match
	}
	return strings.TrimSuffix(packagePrefix.FindString(message), " ")
}

func kindName(message string) string {
	for _, match := range dottedName.FindAllString(message, -1) {
		if !fileAndHostSuffixes[match[strings.LastIndex(match, ".")+1:]] {
			return match
		}
	}
	return ""
}
