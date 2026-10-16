package previewgateway

import (
	"strings"
	"testing"
)

func TestParseStreamsAccepts(t *testing.T) {
	body := `[{"key":"ndi:NODE-01 (matrix-main)","kind":"ndi","ndiName":"NODE-01 (matrix-main)"},
	{"key":"camera:driveway","kind":"rtsp","url":"rtsp://user:pw@192.168.1.20:37379/x"}]`
	specs, err := ParseStreams(strings.NewReader(body))
	if err != nil || len(specs) != 2 {
		t.Fatalf("got %v, %v", specs, err)
	}
}

func TestParseStreamsEmptyArray(t *testing.T) {
	specs, err := ParseStreams(strings.NewReader(`[]`))
	if err != nil || specs == nil || len(specs) != 0 {
		t.Fatalf("got %#v, %v", specs, err)
	}
}

func TestParseStreamsRejects(t *testing.T) {
	cases := map[string]string{
		"not json":       `nope`,
		"not array":      `{"key":"a"}`,
		"trailing":       `[] []`,
		"empty key":      `[{"key":"","kind":"ndi","ndiName":"A"}]`,
		"duplicate":      `[{"key":"a","kind":"ndi","ndiName":"A"},{"key":"a","kind":"ndi","ndiName":"B"}]`,
		"unknown kind":   `[{"key":"a","kind":"exec","url":"x"}]`,
		"empty ndi":      `[{"key":"a","kind":"ndi"}]`,
		"quote in ndi":   `[{"key":"a","kind":"ndi","ndiName":"A\" ! fdsink"}]`,
		"backslash":      `[{"key":"a","kind":"ndi","ndiName":"A\\B"}]`,
		"newline in ndi": `[{"key":"a","kind":"ndi","ndiName":"A\nB"}]`,
		"padded ndi":     `[{"key":"a","kind":"ndi","ndiName":" A"}]`,
		"http url":       `[{"key":"a","kind":"rtsp","url":"http://h/x"}]`,
		"exec url":       `[{"key":"a","kind":"rtsp","url":"exec:rm -rf /"}]`,
		"no host":        `[{"key":"a","kind":"rtsp","url":"rtsp:///x"}]`,
		"fragment":       `[{"key":"a","kind":"rtsp","url":"rtsp://h/x#backchannel=1"}]`,
		"space url":      `[{"key":"a","kind":"rtsp","url":"rtsp://h/x y"}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseStreams(strings.NewReader(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseStreamsTooMany(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i <= maxStreams; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"key":"k` + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + `-` + itoa(i) + `","kind":"ndi","ndiName":"A"}`)
	}
	b.WriteString("]")
	if _, err := ParseStreams(strings.NewReader(b.String())); err == nil {
		t.Fatal("expected an error")
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(digits[i%10]) + s
	}
	return s
}

func TestNDIPipelineArgsKeepNameInOneArgument(t *testing.T) {
	name := "NODE-01 (matrix-main) ; $(rm -rf /) ! fdsink"
	args, err := NDIPipelineArgs(name)
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, a := range args {
		if strings.Contains(a, "rm -rf") {
			found++
			if a != "ndi-name="+name {
				t.Errorf("name is not a single property argument: %q", a)
			}
		}
	}
	if found != 1 {
		t.Fatalf("name appears in %d arguments", found)
	}
	if !contains(args, "bandwidth=0") {
		t.Fatal("the lowest NDI bandwidth is not requested")
	}
	for _, bad := range []string{`A" ! filesink location=/x`, `A\B`, "A\nB", ""} {
		if _, err := NDIPipelineArgs(bad); err == nil {
			t.Errorf("unsafe name %q accepted", bad)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestStreamIDHidesKey(t *testing.T) {
	id := streamID("camera:driveway")
	if strings.Contains(id, "driveway") || id != streamID("camera:driveway") || id == streamID("camera:other") {
		t.Fatalf("bad id %q", id)
	}
}
