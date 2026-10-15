package previewgateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	KindNDI  = "ndi"
	KindRTSP = "rtsp"

	maxStreams    = 256
	maxKeyBytes   = 512
	maxNDIName    = 255
	maxURLBytes   = 2048
	maxStreamBody = 1 << 20
)

// StreamSpec is one desired stream as the coordinator sends it.
type StreamSpec struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	NDIName string `json:"ndiName,omitempty"`
	URL     string `json:"url,omitempty"`
}

// ParseStreams decodes and validates a desired stream set.
func ParseStreams(r io.Reader) ([]StreamSpec, error) {
	var specs []StreamSpec
	dec := json.NewDecoder(io.LimitReader(r, maxStreamBody+1))
	if err := dec.Decode(&specs); err != nil {
		return nil, errors.New("The stream list is not a JSON array of streams. Send an array of key, kind and source objects.")
	}
	if dec.More() {
		return nil, errors.New("The stream list has data after the array. Send only the array.")
	}
	if specs == nil {
		specs = []StreamSpec{}
	}
	if len(specs) > maxStreams {
		return nil, fmt.Errorf("The stream list has more than %d streams. Send fewer streams.", maxStreams)
	}
	seen := make(map[string]bool, len(specs))
	for i, s := range specs {
		if err := validateKey(s.Key); err != nil {
			return nil, fmt.Errorf("Stream %d has an invalid key: %w", i+1, err)
		}
		if seen[s.Key] {
			return nil, fmt.Errorf("The key %q appears twice. Send each key once.", s.Key)
		}
		seen[s.Key] = true
		switch s.Kind {
		case KindNDI:
			if err := ValidateNDIName(s.NDIName); err != nil {
				return nil, fmt.Errorf("Stream %q has an invalid NDI name: %w", s.Key, err)
			}
		case KindRTSP:
			if err := ValidateRTSPURL(s.URL); err != nil {
				return nil, fmt.Errorf("Stream %q has an invalid camera address: %w", s.Key, err)
			}
		default:
			return nil, fmt.Errorf("Stream %q has an unknown kind. Use ndi or rtsp.", s.Key)
		}
	}
	return specs, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validateKey(k string) error {
	switch {
	case k == "":
		return errors.New("The key is empty. Give every stream a key.")
	case len(k) > maxKeyBytes:
		return errors.New("The key is too long. Use a shorter key.")
	case !utf8.ValidString(k) || hasControl(k):
		return errors.New("The key has unreadable characters. Use plain text.")
	}
	return nil
}

// ValidateNDIName accepts the characters NDI source names use in practice and
// refuses the two that could end a quoted GStreamer property value.
func ValidateNDIName(name string) error {
	switch {
	case name == "":
		return errors.New("The NDI name is empty. Give the full source name.")
	case len(name) > maxNDIName:
		return errors.New("The NDI name is too long. Use the name the sender announces.")
	case !utf8.ValidString(name) || hasControl(name):
		return errors.New("The NDI name has unreadable characters. Use the name the sender announces.")
	case strings.ContainsAny(name, "\"\\"):
		return errors.New("The NDI name has a quote or backslash. Rename the NDI source.")
	case name != strings.TrimSpace(name):
		return errors.New("The NDI name starts or ends with a space. Use the name the sender announces.")
	}
	return nil
}

// ValidateRTSPURL accepts only rtsp and rtsps URLs with a host and no
// fragment, because go2rtc reads options from the fragment.
func ValidateRTSPURL(raw string) error {
	if raw == "" || len(raw) > maxURLBytes || !utf8.ValidString(raw) || hasControl(raw) || strings.ContainsAny(raw, " \t#") {
		return errors.New("The address must be one rtsp:// or rtsps:// URL with no spaces or #.")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") || u.Hostname() == "" {
		return errors.New("The address must be one rtsp:// or rtsps:// URL with a host.")
	}
	return nil
}

// streamID is the name go2rtc knows a key by. It is derived from the key so
// that no operator text reaches a go2rtc URL or log line.
func streamID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "k" + hex.EncodeToString(sum[:8])
}

// EncodeNDIName and DecodeNDIName carry an NDI name through a go2rtc exec
// command line as one token with no characters a command parser could split.
func EncodeNDIName(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

func DecodeNDIName(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", errors.New("the NDI name token is not valid base64")
	}
	name := string(raw)
	if err := ValidateNDIName(name); err != nil {
		return "", err
	}
	return name, nil
}
