// Package enrollment mints and redeems node enrollment codes (ADR-055
// decisions 4 to 8) and writes the built-in broker's password file and
// generated access list.
package enrollment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// codeAlphabet is Crockford's base32 alphabet: no I, L, O or U.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const codeLength = 8

// GenerateCode returns a fresh random code formatted as XXXX-XXXX.
func GenerateCode() (string, error) {
	raw := make([]byte, codeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("enrollment: generate code: %w", err)
	}
	var b strings.Builder
	for i, v := range raw {
		if i == codeLength/2 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[v&0x1f])
	}
	return b.String(), nil
}

// NormalizeCode accepts a code in any case, with or without its hyphen,
// and returns its eight upper-case characters. Crockford's substitutions
// apply: O reads as 0, and I and L read as 1.
func NormalizeCode(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) == codeLength+1 && s[codeLength/2] == '-' {
		s = s[:codeLength/2] + s[codeLength/2+1:]
	}
	if len(s) != codeLength {
		return "", false
	}
	out := make([]byte, codeLength)
	for i := 0; i < codeLength; i++ {
		c := s[i]
		switch c {
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if !strings.ContainsRune(codeAlphabet, rune(c)) {
			return "", false
		}
		out[i] = c
	}
	return string(out), true
}

// HashCode returns the hex SHA-256 of a normalized code, the only form of
// a code the coordinator stores.
func HashCode(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
