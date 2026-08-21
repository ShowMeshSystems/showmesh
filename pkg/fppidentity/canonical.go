package fppidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// kind identifies the type of a parsed JSON value. Named to mirror the C++
// json::Type enum in native/include/showmesh/json.h.
type kind int

const (
	kindNull kind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// value is the internal parse tree, equivalent to the C++ json::Value. It
// is not exported: callers only ever see the canonical bytes or the
// derived hashes.
type value struct {
	k       kind
	b       bool
	n       float64
	s       string
	items   []value
	members []member
}

type member struct {
	name string
	v    value
}

func makeNull() value                   { return value{k: kindNull} }
func makeBool(b bool) value             { return value{k: kindBool, b: b} }
func makeNumber(n float64) value        { return value{k: kindNumber, n: n} }
func makeString(s string) value         { return value{k: kindString, s: s} }
func makeArray(items []value) value     { return value{k: kindArray, items: items} }
func makeObject(members []member) value { return value{k: kindObject, members: members} }

// maxDepth bounds recursive array/object nesting, matching the C++
// parser's kMaxDepth: an attacker-controlled document cannot exhaust the
// goroutine stack.
const maxDepth = 200

// parser is a hand-written recursive-descent JSON parser mirroring
// native/src/json.cpp's Parser byte for byte: same grammar, same
// duplicate-member rejection, same JSON number grammar. It exists because
// encoding/json cannot preserve exact number literals or reject duplicate
// object keys; see doc.go.
type parser struct {
	text  string
	pos   int
	depth int
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf(format+" at offset %d", append(args, p.pos)...)
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) literal(word string) error {
	if p.pos+len(word) > len(p.text) || p.text[p.pos:p.pos+len(word)] != word {
		return p.errf("unrecognized literal")
	}
	p.pos += len(word)
	return nil
}

func (p *parser) parseValue() (value, error) {
	p.depth++
	if p.depth > maxDepth {
		return value{}, p.errf("JSON nesting is too deep")
	}
	defer func() { p.depth-- }()
	if p.pos >= len(p.text) {
		return value{}, p.errf("unexpected end of input")
	}
	switch p.text[p.pos] {
	case 'n':
		if err := p.literal("null"); err != nil {
			return value{}, err
		}
		return makeNull(), nil
	case 't':
		if err := p.literal("true"); err != nil {
			return value{}, err
		}
		return makeBool(true), nil
	case 'f':
		if err := p.literal("false"); err != nil {
			return value{}, err
		}
		return makeBool(false), nil
	case '"':
		s, err := p.parseString()
		if err != nil {
			return value{}, err
		}
		return makeString(s), nil
	case '[':
		return p.parseArray()
	case '{':
		return p.parseObject()
	default:
		return p.parseNumber()
	}
}

func (p *parser) parseArray() (value, error) {
	p.pos++ // '['
	var items []value
	p.skipWhitespace()
	if p.pos < len(p.text) && p.text[p.pos] == ']' {
		p.pos++
		return makeArray(items), nil
	}
	for {
		p.skipWhitespace()
		v, err := p.parseValue()
		if err != nil {
			return value{}, err
		}
		items = append(items, v)
		p.skipWhitespace()
		if p.pos >= len(p.text) {
			return value{}, p.errf("unterminated array")
		}
		switch p.text[p.pos] {
		case ',':
			p.pos++
			continue
		case ']':
			p.pos++
			return makeArray(items), nil
		default:
			return value{}, p.errf("expected ',' or ']'")
		}
	}
}

func (p *parser) parseObject() (value, error) {
	p.pos++ // '{'
	var members []member
	p.skipWhitespace()
	if p.pos < len(p.text) && p.text[p.pos] == '}' {
		p.pos++
		return makeObject(members), nil
	}
	for {
		p.skipWhitespace()
		if p.pos >= len(p.text) || p.text[p.pos] != '"' {
			return value{}, p.errf("expected a member name")
		}
		name, err := p.parseString()
		if err != nil {
			return value{}, err
		}
		for _, m := range members {
			if m.name == name {
				return value{}, p.errf("duplicate object member name")
			}
		}
		p.skipWhitespace()
		if p.pos >= len(p.text) || p.text[p.pos] != ':' {
			return value{}, p.errf("expected ':'")
		}
		p.pos++
		p.skipWhitespace()
		v, err := p.parseValue()
		if err != nil {
			return value{}, err
		}
		members = append(members, member{name: name, v: v})
		p.skipWhitespace()
		if p.pos >= len(p.text) {
			return value{}, p.errf("unterminated object")
		}
		switch p.text[p.pos] {
		case ',':
			p.pos++
			continue
		case '}':
			p.pos++
			return makeObject(members), nil
		default:
			return value{}, p.errf("expected ',' or '}'")
		}
	}
}

func (p *parser) parseHex4() (uint32, error) {
	if p.pos+4 > len(p.text) {
		return 0, p.errf("truncated \\u escape")
	}
	var v uint32
	for i := 0; i < 4; i++ {
		c := p.text[p.pos+i]
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		default:
			return 0, p.errf("invalid \\u escape")
		}
	}
	p.pos += 4
	return v, nil
}

func appendCodePoint(cp uint32, out *strings.Builder) {
	switch {
	case cp < 0x80:
		out.WriteByte(byte(cp))
	case cp < 0x800:
		out.WriteByte(byte(0xC0 | (cp >> 6)))
		out.WriteByte(byte(0x80 | (cp & 0x3F)))
	case cp < 0x10000:
		out.WriteByte(byte(0xE0 | (cp >> 12)))
		out.WriteByte(byte(0x80 | ((cp >> 6) & 0x3F)))
		out.WriteByte(byte(0x80 | (cp & 0x3F)))
	default:
		out.WriteByte(byte(0xF0 | (cp >> 18)))
		out.WriteByte(byte(0x80 | ((cp >> 12) & 0x3F)))
		out.WriteByte(byte(0x80 | ((cp >> 6) & 0x3F)))
		out.WriteByte(byte(0x80 | (cp & 0x3F)))
	}
}

func (p *parser) parseString() (string, error) {
	p.pos++ // opening quote
	var out strings.Builder
	for {
		if p.pos >= len(p.text) {
			return "", p.errf("unterminated string")
		}
		c := p.text[p.pos]
		if c == '"' {
			p.pos++
			return out.String(), nil
		}
		if c < 0x20 {
			return "", p.errf("unescaped control character in a string")
		}
		if c != '\\' {
			out.WriteByte(c)
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.text) {
			return "", p.errf("unterminated escape")
		}
		e := p.text[p.pos]
		p.pos++
		switch e {
		case '"':
			out.WriteByte('"')
		case '\\':
			out.WriteByte('\\')
		case '/':
			out.WriteByte('/')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'u':
			cp, err := p.parseHex4()
			if err != nil {
				return "", err
			}
			if cp >= 0xD800 && cp <= 0xDBFF {
				if p.pos+1 < len(p.text) && p.text[p.pos] == '\\' && p.text[p.pos+1] == 'u' {
					p.pos += 2
					low, err := p.parseHex4()
					if err != nil {
						return "", err
					}
					if low < 0xDC00 || low > 0xDFFF {
						return "", p.errf("invalid low surrogate")
					}
					cp = 0x10000 + ((cp - 0xD800) << 10) + (low - 0xDC00)
				} else {
					return "", p.errf("unpaired high surrogate")
				}
			} else if cp >= 0xDC00 && cp <= 0xDFFF {
				return "", p.errf("unpaired low surrogate")
			}
			appendCodePoint(cp, &out)
		default:
			return "", p.errf("unrecognized escape")
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func (p *parser) parseNumber() (value, error) {
	start := p.pos
	if p.pos < len(p.text) && p.text[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.text) {
		return value{}, p.errf("truncated number")
	}
	switch {
	case p.text[p.pos] == '0':
		p.pos++
	case p.text[p.pos] >= '1' && p.text[p.pos] <= '9':
		for p.pos < len(p.text) && isDigit(p.text[p.pos]) {
			p.pos++
		}
	default:
		return value{}, p.errf("expected a JSON value")
	}
	if p.pos < len(p.text) && p.text[p.pos] == '.' {
		p.pos++
		if p.pos >= len(p.text) || !isDigit(p.text[p.pos]) {
			return value{}, p.errf("truncated fraction")
		}
		for p.pos < len(p.text) && isDigit(p.text[p.pos]) {
			p.pos++
		}
	}
	if p.pos < len(p.text) && (p.text[p.pos] == 'e' || p.text[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
			p.pos++
		}
		if p.pos >= len(p.text) || !isDigit(p.text[p.pos]) {
			return value{}, p.errf("truncated exponent")
		}
		for p.pos < len(p.text) && isDigit(p.text[p.pos]) {
			p.pos++
		}
	}
	literalText := p.text[start:p.pos]
	d, err := strconv.ParseFloat(literalText, 64)
	// ParseFloat returns ErrRange with the closest representable value
	// (±Inf for overflow) rather than failing outright; isFinite below is
	// what actually rejects an out-of-range literal, matching the C++
	// parser's std::isfinite check on the strtod result.
	if err != nil {
		var numErr *strconv.NumError
		if !errors.As(err, &numErr) || !errors.Is(numErr.Err, strconv.ErrRange) {
			return value{}, p.errf("malformed number literal")
		}
	}
	if math.IsInf(d, 0) || math.IsNaN(d) {
		return value{}, p.errf("number is out of the range JSON can carry")
	}
	return makeNumber(d), nil
}

func parseJSON(text string) (value, error) {
	p := &parser{text: text}
	p.skipWhitespace()
	v, err := p.parseValue()
	if err != nil {
		return value{}, err
	}
	p.skipWhitespace()
	if p.pos != len(p.text) {
		return value{}, p.errf("trailing content after the JSON value")
	}
	return v, nil
}

// toUTF16 decodes s (assumed UTF-8) into UTF-16 code units, so member
// names can be compared the way RFC 8785 requires: by UTF-16 code unit,
// not by UTF-8 byte. The two orderings disagree for any supplementary
// character (U+10000 and above), which sorts after U+E000..U+FFFF in raw
// UTF-8 bytes but before it in UTF-16, because its surrogate pair starts
// at 0xD800. Mirrors native/src/json.cpp's toUtf16.
func toUTF16(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		var cp uint32
		var length int
		switch {
		case c < 0x80:
			cp = uint32(c)
			length = 1
		case c&0xE0 == 0xC0:
			cp = uint32(c & 0x1F)
			length = 2
		case c&0xF0 == 0xE0:
			cp = uint32(c & 0x0F)
			length = 3
		case c&0xF8 == 0xF0:
			cp = uint32(c & 0x07)
			length = 4
		default:
			// Not a valid UTF-8 lead byte: treat as a single code unit so
			// ordering stays total rather than erroring here.
			out = append(out, uint16(c))
			i++
			continue
		}
		if i+length > len(s) {
			out = append(out, uint16(c))
			i++
			continue
		}
		for k := 1; k < length; k++ {
			cp = (cp << 6) | uint32(s[i+k]&0x3F)
		}
		i += length
		if cp >= 0x10000 {
			cp -= 0x10000
			out = append(out, uint16(0xD800+(cp>>10)))
			out = append(out, uint16(0xDC00+(cp&0x3FF)))
		} else {
			out = append(out, uint16(cp))
		}
	}
	return out
}

func lessUTF16(a, b string) bool {
	au, bu := toUTF16(a), toUTF16(b)
	n := len(au)
	if len(bu) < n {
		n = len(bu)
	}
	for i := 0; i < n; i++ {
		if au[i] != bu[i] {
			return au[i] < bu[i]
		}
	}
	return len(au) < len(bu)
}

// appendEscaped writes s as a minimally-escaped JSON string: only `"`,
// `\`, and the C0 control characters need an escape, and the six named
// escapes are used where they exist. Everything else, including non-ASCII
// text and `/`, is written through unescaped. Mirrors appendEscaped in
// native/src/json.cpp.
func appendEscaped(s string, out *strings.Builder) {
	out.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(out, `\u%04x`, c)
			} else {
				out.WriteByte(c)
			}
		}
	}
	out.WriteByte('"')
}

// formatNumber implements ECMAScript's Number::toString, which RFC 8785
// adopts wholesale: find the shortest decimal digit string that
// round-trips to v, then place it per the k/n/21/-6 branch structure.
// strconv.FormatFloat(v, 'g', -1, 64) is not this format (its exponent
// threshold and trailing-zero behavior both differ), so this cannot be
// delegated to it. Mirrors formatNumber in native/src/json.cpp.
func formatNumber(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", errors.New("fppidentity: cannot canonicalize NaN or Inf")
	}
	if v == 0 {
		return "0", nil // negative zero is also "0"
	}

	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}

	var formatted string
	precision := 1
	for ; precision <= 17; precision++ {
		formatted = strconv.FormatFloat(v, 'e', precision-1, 64)
		back, err := strconv.ParseFloat(formatted, 64)
		if err == nil && back == v {
			break
		}
	}
	if precision > 17 {
		formatted = strconv.FormatFloat(v, 'e', 16, 64)
	}

	// formatted is "d[.ddd]e±XX": split into the digit string and the
	// decimal exponent n, where the value is 0.<digits> * 10^n.
	epos := strings.IndexByte(formatted, 'e')
	mantissa := formatted[:epos]
	exponent, err := strconv.Atoi(formatted[epos+1:])
	if err != nil {
		return "", fmt.Errorf("fppidentity: could not parse exponent of %q: %w", formatted, err)
	}

	var digitsB strings.Builder
	for _, c := range mantissa {
		if c != '.' {
			digitsB.WriteRune(c)
		}
	}
	digits := digitsB.String()
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
	}

	k := len(digits)
	n := exponent + 1

	var body string
	switch {
	case k <= n && n <= 21:
		body = digits + strings.Repeat("0", n-k)
	case n > 0 && n <= 21:
		body = digits[:n] + "." + digits[n:]
	case n > -6 && n <= 0:
		body = "0." + strings.Repeat("0", -n) + digits
	default:
		e := n - 1
		var expPart string
		if e >= 0 {
			expPart = "e+" + strconv.Itoa(e)
		} else {
			expPart = "e-" + strconv.Itoa(-e)
		}
		if k == 1 {
			body = digits + expPart
		} else {
			body = digits[:1] + "." + digits[1:] + expPart
		}
	}

	return sign + body, nil
}

// writeValue serializes v as RFC 8785 JCS: sorted object members, no
// insignificant whitespace, ECMAScript number formatting, minimal string
// escaping. Mirrors writeValue in native/src/json.cpp.
func writeValue(v value, out *strings.Builder) error {
	switch v.k {
	case kindNull:
		out.WriteString("null")
		return nil
	case kindBool:
		if v.b {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		return nil
	case kindNumber:
		formatted, err := formatNumber(v.n)
		if err != nil {
			return fmt.Errorf("fppidentity: a number could not be canonicalized: %w", err)
		}
		out.WriteString(formatted)
		return nil
	case kindString:
		appendEscaped(v.s, out)
		return nil
	case kindArray:
		out.WriteByte('[')
		for i, item := range v.items {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeValue(item, out); err != nil {
				return err
			}
		}
		out.WriteByte(']')
		return nil
	case kindObject:
		sorted := make([]member, len(v.members))
		copy(sorted, v.members)
		sortMembers(sorted)
		out.WriteByte('{')
		for i, m := range sorted {
			if i > 0 {
				out.WriteByte(',')
			}
			appendEscaped(m.name, out)
			out.WriteByte(':')
			if err := writeValue(m.v, out); err != nil {
				return err
			}
		}
		out.WriteByte('}')
		return nil
	default:
		return errors.New("fppidentity: unknown JSON value type")
	}
}

// sortMembers sorts object members by UTF-16 code unit, per RFC 8785.
// Insertion sort is intentional: the object shapes in this contract
// (playlist definitions, five-field entry keys) are small enough that
// stdlib sort's setup cost is not worth pulling in here, and Go's sort
// package is not otherwise used by this package.
func sortMembers(members []member) {
	for i := 1; i < len(members); i++ {
		for j := i; j > 0 && lessUTF16(members[j].name, members[j-1].name); j-- {
			members[j], members[j-1] = members[j-1], members[j]
		}
	}
}

// Canonicalize parses raw as RFC 8259 JSON and re-serializes it per RFC
// 8785 JCS: sorted object members, no insignificant whitespace,
// ECMAScript number formatting, minimal string escaping. Duplicate object
// member names are rejected, since JCS gives no canonical answer for which
// one should survive. Malformed JSON, NaN, and infinities are rejected.
func Canonicalize(raw []byte) ([]byte, error) {
	v, err := parseJSON(string(raw))
	if err != nil {
		return nil, fmt.Errorf("fppidentity: %w", err)
	}
	var out strings.Builder
	if err := writeValue(v, &out); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// HashCanonical canonicalizes raw and returns both the canonical bytes and
// their lowercase-hex SHA-256, which is the playlistHash derivation
// described in contract section 1.3: SHA-256 over the RFC 8785
// canonicalization of the complete playlist definition FPP returned, with
// no runtime field removed before hashing.
func HashCanonical(raw []byte) (canonical []byte, hashHex string, err error) {
	canonical, err = Canonicalize(raw)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}
