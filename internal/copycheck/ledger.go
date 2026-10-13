package copycheck

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	// UpdateEnv, when set to 1, makes a full passing run rewrite the list.
	UpdateEnv = "SHOWMESH_COPYCHECK_UPDATE"
	// ReportEnv names a file that receives every violating message seen.
	ReportEnv = "SHOWMESH_COPYCHECK_REPORT"

	exemptHeading = "exempt by ruling"
)

// MessageFields are the JSON field names whose strings a person reads.
var MessageFields = map[string]bool{
	"reason": true, "reasons": true, "warning": true, "warnings": true, "outcomeReason": true,
	"unalignedReason": true, "message": true, "summary": true, "degradedReason": true, "failureReason": true,
}

// A config validation error names its field first. Those are exempt by the
// owner's ruling and are listed under their own heading.
var fieldPathMust = regexp.MustCompile(`^[a-z][A-Za-z0-9_]*([.\[][^ ]*)? must `)

// Exempt reports whether message is a "<fieldPath> must ..." validation error.
func Exempt(message string) bool { return fieldPathMust.MatchString(message) }

type entry struct {
	exempt bool
	rule   string
	key    string
}

func (e entry) String() string { return e.rule + "\t" + e.key }

// Ledger collects the violations one test binary produces and holds them
// against a checked-in list that may only shrink. It is safe for concurrent use.
type Ledger struct {
	path string

	mu       sync.Mutex
	messages map[string]struct{}
	seen     map[entry]map[string]struct{}
}

// NewLedger returns a Ledger whose list lives at path.
func NewLedger(path string) *Ledger {
	return &Ledger{path: path, messages: map[string]struct{}{}, seen: map[entry]map[string]struct{}{}}
}

// Observe checks one produced message. key says where it came from and must
// not carry values that change from run to run.
func (l *Ledger) Observe(key, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	violations := Check(message)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[message] = struct{}{}
	for _, v := range violations {
		e := entry{exempt: Exempt(message), rule: v.Rule, key: key}
		if l.seen[e] == nil {
			l.seen[e] = map[string]struct{}{}
		}
		l.seen[e][message] = struct{}{}
	}
}

// ObserveJSON checks every string held by a field named in MessageFields, at
// any depth of body. The key of each is prefix plus the field's path.
func (l *Ledger) ObserveJSON(prefix string, body []byte) {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return
	}
	l.walk(prefix, "", value)
}

func (l *Ledger) walk(prefix, path string, value any) {
	switch v := value.(type) {
	case map[string]any:
		for name, child := range v {
			childPath := strings.TrimPrefix(path+"."+name, ".")
			if MessageFields[name] {
				l.observeField(prefix+" "+childPath, child)
			}
			l.walk(prefix, childPath, child)
		}
	case []any:
		for _, child := range v {
			l.walk(prefix, path+"[]", child)
		}
	}
}

func (l *Ledger) observeField(key string, value any) {
	switch v := value.(type) {
	case string:
		l.Observe(key, v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				l.Observe(key, s)
			}
		}
	}
}

// Settle is called from TestMain with m.Run's result and returns the exit
// code. A violation missing from the list fails the run. So does a listed
// entry a full run no longer produces.
func (l *Ledger) Settle(code int) int { return l.settle(code, filtered(), os.Stdout) }

func (l *Ledger) settle(code int, partial bool, out io.Writer) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.writeReport(out)
	if os.Getenv(UpdateEnv) == "1" {
		if partial || code != 0 {
			say(out, "copycheck: %s not rewritten, because only a full passing run can rewrite it.\n", l.path)
			return code
		}
		if err := os.WriteFile(l.path, []byte(l.render()), 0o644); err != nil {
			say(out, "copycheck: cannot write %s: %v\n", l.path, err)
			return 1
		}
		say(out, "copycheck: rewrote %s with %d entries.\n", l.path, len(l.seen))
		return code
	}

	listed, err := readList(l.path)
	if err != nil {
		say(out, "copycheck: cannot read %s: %v\n", l.path, err)
		return 1
	}
	failed := false
	for _, e := range sortedEntries(l.seen) {
		if listed[e] {
			continue
		}
		failed = true
		for _, message := range sortedKeys(l.seen[e]) {
			say(out, "copycheck: new violation\n    message: %s\n    rule:    %s\n    key:     %s\n", message, e.rule, e.key)
		}
	}
	if !partial && code == 0 {
		for _, e := range sortedEntries(listed) {
			if _, still := l.seen[e]; !still {
				failed = true
				say(out, "copycheck: listed violation no longer occurs, remove it from %s\n    rule:    %s\n    key:     %s\n", l.path, e.rule, e.key)
			}
		}
	}
	say(out, "copycheck: %d distinct messages checked, %d known violations listed in %s.\n", len(l.messages), len(listed), l.path)
	if failed {
		say(out, "copycheck: FAILED. Fix the message, or after fixing listed ones rerun the whole package with %s=1.\n", UpdateEnv)
		if code == 0 {
			return 1
		}
	}
	return code
}

func (l *Ledger) render() string {
	var b strings.Builder
	b.WriteString("# Known copy violations in messages this package's tests produce.\n")
	b.WriteString("# One line per rule and source, tab separated. This list only shrinks:\n")
	b.WriteString("# a violation that is not here fails the tests, and so does a line here\n")
	b.WriteString("# that a full run no longer produces. After fixing messages, rerun the\n")
	b.WriteString("# whole package with " + UpdateEnv + "=1 to rewrite it.\n")
	var exempt []entry
	for _, e := range sortedEntries(l.seen) {
		if e.exempt {
			exempt = append(exempt, e)
			continue
		}
		b.WriteString(e.String() + "\n")
	}
	b.WriteString("\n# " + exemptHeading + "\n")
	b.WriteString("# Config validation errors of the form \"<fieldPath> must ...\".\n")
	for _, e := range exempt {
		b.WriteString(e.String() + "\n")
	}
	return b.String()
}

func (l *Ledger) writeReport(out io.Writer) {
	path := os.Getenv(ReportEnv)
	if path == "" {
		return
	}
	var b strings.Builder
	for _, e := range sortedEntries(l.seen) {
		section := "known"
		if e.exempt {
			section = "exempt"
		}
		for _, message := range sortedKeys(l.seen[e]) {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%q\n", section, e.rule, e.key, message)
		}
	}
	fmt.Fprintf(&b, "checked\t%d\n", len(l.messages))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		say(out, "copycheck: cannot write report %s: %v\n", path, err)
	}
}

func readList(path string) (map[entry]bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[entry]bool{}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := map[entry]bool{}
	exempt := false
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(strings.TrimPrefix(line, "#")) == exemptHeading {
			exempt = true
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule, key, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("line %q is not rule, tab, key", line)
		}
		listed[entry{exempt: exempt, rule: rule, key: key}] = true
	}
	return listed, nil
}

func say(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// filtered reports whether this test binary was asked to run only part of
// its package, in which case a listed entry may simply not have been reached.
func filtered() bool {
	for _, name := range []string{"test.run", "test.skip", "test.list", "test.bench", "test.fuzz"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return true
		}
	}
	short := flag.Lookup("test.short")
	return short != nil && short.Value.String() == "true"
}

func sortedEntries[V any](set map[entry]V) []entry {
	entries := make([]entry, 0, len(set))
	for e := range set {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.exempt != b.exempt {
			return !a.exempt
		}
		if a.key != b.key {
			return a.key < b.key
		}
		return a.rule < b.rule
	})
	return entries
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
