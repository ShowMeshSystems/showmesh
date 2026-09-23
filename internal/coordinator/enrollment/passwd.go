package enrollment

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Mosquitto 2.0's mosquitto_passwd writes username:$7$<iterations>$<salt>$<hash>,
// PBKDF2-HMAC-SHA512 with a 12-byte salt, 101 iterations and a 64-byte key,
// both encoded as padded standard base64.
const (
	passwdSaltBytes  = 12
	passwdIterations = 101
	passwdKeyBytes   = 64
)

// passwdEntry returns one password file line, without its newline.
func passwdEntry(username, password string) (string, error) {
	salt := make([]byte, passwdSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("enrollment: generate password salt: %w", err)
	}
	return passwdEntryWithSalt(username, password, salt, passwdIterations)
}

func passwdEntryWithSalt(username, password string, salt []byte, iterations int) (string, error) {
	key, err := pbkdf2.Key(sha512.New, password, salt, iterations, passwdKeyBytes)
	if err != nil {
		return "", fmt.Errorf("enrollment: hash password: %w", err)
	}
	return fmt.Sprintf("%s:$7$%d$%s$%s", username, iterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}

// verifyPasswdEntry reports whether entry (one line, no newline) is a
// $7$ entry for username that accepts password.
func verifyPasswdEntry(entry, username, password string) bool {
	user, hash, ok := strings.Cut(entry, ":")
	if !ok || user != username {
		return false
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "7" {
		return false
	}
	iterations, err := strconv.Atoi(parts[2])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := passwdEntryWithSalt(username, password, salt, iterations)
	return err == nil && want == entry
}

// passwdUsernames returns the username of every newline-terminated line,
// the lines the ACL renderer's shell loop reads.
func passwdUsernames(passwd []byte) []string {
	var out []string
	for len(passwd) > 0 {
		i := bytes.IndexByte(passwd, '\n')
		if i < 0 {
			break
		}
		line := string(passwd[:i])
		passwd = passwd[i+1:]
		user, _, _ := strings.Cut(line, ":")
		out = append(out, user)
	}
	return out
}

// passwdHasUser reports whether any line of passwd names username.
func passwdHasUser(passwd []byte, username string) bool {
	for _, line := range strings.Split(string(passwd), "\n") {
		if user, _, _ := strings.Cut(line, ":"); user == username {
			return true
		}
	}
	return false
}

// upsertPasswdEntry replaces username's line in passwd with entry, or
// appends entry, leaving every other line byte for byte as it was.
func upsertPasswdEntry(passwd []byte, username, entry string) []byte {
	var out bytes.Buffer
	replaced := false
	rest := passwd
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		var line []byte
		if i < 0 {
			line, rest = rest, nil
		} else {
			line, rest = rest[:i], rest[i+1:]
		}
		user, _, _ := bytes.Cut(line, []byte(":"))
		if string(user) == username && !replaced {
			out.WriteString(entry)
			replaced = true
		} else if string(user) != username {
			out.Write(line)
		} else {
			continue
		}
		out.WriteByte('\n')
	}
	if !replaced {
		out.WriteString(entry)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
