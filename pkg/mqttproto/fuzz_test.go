package mqttproto

import "testing"

// FuzzDecodeEnvelope asserts only that DecodeEnvelope never panics on
// arbitrary input, matching pkg/multisync/fuzz_test.go's FuzzDecode. This
// package parses untrusted JSON that arrived over MQTT off a show network;
// a crafted or corrupted message must always come back as an error, never
// a panic.
func FuzzDecodeEnvelope(f *testing.F) {
	f.Add([]byte(`{"schema":"showmesh.node.hello/v1","messageId":"11111111-1111-4111-8111-111111111111","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":{}}`))
	f.Add([]byte(`{"schema":"showmesh.node.health/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":{"sequence":1}}`))
	f.Add([]byte(`{"schema":"showmesh.node.lwt/v1","messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z","payload":{"online":false,"reason":"unexpected disconnect"}}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"schema":123,"messageId":"m","nodeId":"media-03","sentAt":"2026-08-10T12:00:00Z"}`))
	f.Add([]byte(`{"schema":"x","messageId":"m","nodeId":"../../etc/passwd","sentAt":"2026-08-10T12:00:00Z"}`))
	f.Add([]byte(`{"schema":"x","messageId":"m","nodeId":"media-03","sentAt":"not-a-time"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeEnvelope panicked on input %q: %v", data, r)
			}
		}()
		env, err := DecodeEnvelope(data)
		if err != nil {
			return
		}
		// A successfully decoded envelope's payload decoders must also
		// never panic, regardless of Schema or Payload content.
		_, _ = DecodeHelloPayload(env)
		_, _ = DecodeHealthPayload(env)
		_, _ = DecodeLWTPayload(env)
	})
}
