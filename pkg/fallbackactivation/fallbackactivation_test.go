package fallbackactivation

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testRequest() Request {
	return Request{
		SchemaVersion: SchemaVersion, ExecutionID: "00000000-0000-4000-8000-000000000001",
		FPPInstanceUUID: "fpp-1", PackageID: "pkg-1", PackageRevision: "rev-1",
		ProgramExpiresAt: time.Date(2026, 10, 5, 12, 15, 0, 0, time.UTC), Generation: 7,
		CatalogRevision: "cat-1", EntryKey: "entry-0", CueID: "cue-a", CueRevision: 3, NodeID: "node-a",
	}
}

func testKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
}

func TestSignedRequestDecodesAndVerifies(t *testing.T) {
	key := testKey()
	body, err := Sign(testRequest(), key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	received, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if received.Request != testRequest() {
		t.Fatalf("decoded request = %+v, want what was signed", received.Request)
	}
	if err := received.Verify(key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	other := ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", ed25519.SeedSize)))
	if err := received.Verify(other.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("a request verified against a key that did not sign it")
	}
}

// The signature covers the request as it arrived, so a sender may order
// its members and space them however it likes.
func TestVerifyIsOverTheReceivedObjectWhateverItsLayout(t *testing.T) {
	key := testKey()
	canonical, err := testRequest().CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &members); err != nil {
		t.Fatalf("decode canonical request: %v", err)
	}
	var loose strings.Builder
	loose.WriteString("{\n")
	for _, name := range []string{"nodeId", "schemaVersion", "cueRevision", "cueId", "entryKey", "catalogRevision",
		"generation", "programExpiresAt", "packageRevision", "packageId", "fppInstanceUuid", "executionId"} {
		loose.WriteString(`  "` + name + `" : ` + string(members[name]))
		if name != "executionId" {
			loose.WriteString(",")
		}
		loose.WriteString("\n")
	}
	loose.WriteString("}")
	signature, _ := json.Marshal(ed25519.Sign(key, canonical))
	received, err := Decode([]byte(`{"signature":` + string(signature) + `,"request":` + loose.String() + `}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := received.Verify(key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("a reordered, respaced request did not verify: %v", err)
	}
}

func TestDecodeRefusesEverythingButTheFixedShape(t *testing.T) {
	valid, err := Sign(testRequest(), testKey())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(valid, &envelope); err != nil {
		t.Fatalf("decode valid body: %v", err)
	}
	withRequest := func(mutate func(map[string]any)) string {
		var members map[string]any
		_ = json.Unmarshal(envelope["request"], &members)
		mutate(members)
		request, _ := json.Marshal(members)
		return `{"request":` + string(request) + `,"signature":` + string(envelope["signature"]) + `}`
	}
	cases := map[string]string{
		"not JSON":                  `nope`,
		"an array":                  `[]`,
		"no signature":              `{"request":` + string(envelope["request"]) + `}`,
		"a third top-level member":  `{"request":` + string(envelope["request"]) + `,"signature":` + string(envelope["signature"]) + `,"command":"x"}`,
		"an extra request member":   withRequest(func(m map[string]any) { m["action"] = "blackout" }),
		"a missing request member":  withRequest(func(m map[string]any) { delete(m, "nodeId") }),
		"another schema version":    withRequest(func(m map[string]any) { m["schemaVersion"] = 2 }),
		"an uppercase execution id": withRequest(func(m map[string]any) { m["executionId"] = "00000000-0000-4000-8000-00000000000A" }),
		"a free-text execution id":  withRequest(func(m map[string]any) { m["executionId"] = "../../etc/passwd" }),
		"an empty Cue":              withRequest(func(m map[string]any) { m["cueId"] = "" }),
		"a zero generation":         withRequest(func(m map[string]any) { m["generation"] = 0 }),
		"a wrong-typed member":      withRequest(func(m map[string]any) { m["cueRevision"] = "3" }),
		"an unparseable expiry":     withRequest(func(m map[string]any) { m["programExpiresAt"] = "soon" }),
	}
	for name, body := range cases {
		if _, err := Decode([]byte(body)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: Decode error = %v, want ErrMalformed", name, err)
		}
	}
}
