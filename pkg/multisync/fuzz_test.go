package multisync

import "testing"

// FuzzDecode asserts only that Decode never panics on arbitrary input. This
// package parses untrusted UDP off a show network; a crafted or corrupted
// packet must always come back as an error, never a panic, and Decode's own
// bounds-checked reads and length-clamped field parsing are what this fuzz
// target is meant to shake loose any gaps in.
//
// Seeds cover every packet type this package decodes plus a handful of
// deliberately malformed inputs, so the mutator starts from bytes shaped
// like real packets instead of starting cold from nothing.
func FuzzDecode(f *testing.F) {
	// Valid packets, one per type, taken from packet_test.go's hand-built
	// golden bytes.
	f.Add([]byte{ // Blank (0x03), no body
		'F', 'P', 'P', 'D', 0x03, 0x00, 0x00,
	})
	f.Add([]byte{ // Sync (0x01): start sequence, frame0, seconds0, "test.fseq"
		'F', 'P', 'P', 'D', 0x01, 0x14, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x74, 0x65, 0x73, 0x74, 0x2e, 0x66, 0x73,
		0x65, 0x71, 0x00,
	})
	f.Add([]byte{ // Sync (0x01): sync media, frame150, seconds5.25, "show.mp4"
		'F', 'P', 'P', 'D', 0x01, 0x13, 0x00, 0x02, 0x01, 0x96, 0x00, 0x00,
		0x00, 0x00, 0x00, 0xa8, 0x40, 0x73, 0x68, 0x6f, 0x77, 0x2e, 0x6d, 0x70,
		0x34, 0x00,
	})
	f.Add([]byte{ // Ping (0x04): short/legacy-shaped, only fixed fields plus a truncated hostname
		'F', 'P', 'P', 'D', 0x04, 0x0f, 0x00, 0x01, 0x00, 0x01, 0x00, 0x01,
		0x00, 0x0a, 0x02, 0x0a, 0x00, 0x00, 0x05, 0x61, 0x62, 0x63,
	})
	f.Add([]byte{ // Command (0x06): two args, empty host
		'F', 'P', 'P', 'D', 0x06, 0x15, 0x00, 0x02, 0x00, 0x53, 0x74, 0x6f,
		0x70, 0x20, 0x41, 0x6c, 0x6c, 0x20, 0x4e, 0x6f, 0x77, 0x00, 0x61, 0x00,
		0x62, 0x65, 0x65, 0x00,
	})
	f.Add([]byte{ // Plugin (0x05): name plus data
		'F', 'P', 'P', 'D', 0x05, 0x0c, 0x00, 0x6d, 0x79, 0x70, 0x6c, 0x75,
		0x67, 0x69, 0x6e, 0x00, 0x01, 0x02, 0x03,
	})

	// Deliberately malformed inputs.
	f.Add([]byte{})                                                 // empty
	f.Add([]byte{'F', 'P'})                                         // too short for the header
	f.Add([]byte{'X', 'P', 'P', 'D', 0x03, 0x00, 0x00})             // bad magic
	f.Add([]byte{'F', 'P', 'P', 'D', 0x03, 0x05, 0x00, 0x01, 0x02}) // declared length mismatch
	f.Add([]byte{'F', 'P', 'P', 'D', 0x01, 0x00, 0x00})             // Sync with zero-length body
	f.Add([]byte{'F', 'P', 'P', 'D', 0x04, 0x00, 0x00})             // Ping with zero-length body
	f.Add([]byte{'F', 'P', 'P', 'D', 0x06, 0x00, 0x00})             // Command with zero-length body
	f.Add([]byte{'F', 'P', 'P', 'D', 0x02, 0x00, 0x00})             // unknown (deprecated Event) type
	f.Add([]byte{                                                   // Sync with no null terminator anywhere in the filename
		'F', 'P', 'P', 'D', 0x01, 0x0d, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x61, 0x62, 0x63,
	})
	f.Add([]byte{ // Command missing a null terminator
		'F', 'P', 'P', 'D', 0x06, 0x0c, 0x00, 0x00, 0x00, 0x4e, 0x6f, 0x4e,
		0x75, 0x6c, 0x6c, 0x48, 0x65, 0x72, 0x65,
	})
	// ExtraDataLen declaring far more than a UDP datagram could plausibly
	// carry, with no body at all to back it up.
	f.Add([]byte{'F', 'P', 'P', 'D', 0x01, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Decode panicked on input % x: %v", data, r)
			}
		}()
		_, _, _ = Decode(data)
	})
}
