package weatherdelay

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
)

func TestStateValidateActive(t *testing.T) {
	s := State{Active: true, Kind: KindDelay, StartedAt: time.Now().UTC(), StartedBy: "op-1", Revision: 1}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestStateValidateNotActive(t *testing.T) {
	if err := NotActive.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestStateValidateRejectsBadKind(t *testing.T) {
	s := State{Active: true, Kind: "resume", StartedAt: time.Now(), StartedBy: "op-1"}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for unknown kind")
	}
}

func TestStateValidateRejectsInactiveWithLeftoverFields(t *testing.T) {
	cases := []State{
		{Kind: KindDelay},
		{StartedBy: "op-1"},
		{StartedAt: time.Now()},
	}
	for _, s := range cases {
		if err := s.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want error", s)
		}
	}
}

func TestStateValidateRejectsRevisionOutOfRange(t *testing.T) {
	cases := []int64{-1, MaxSafeRevision + 1}
	for _, rev := range cases {
		s := State{Revision: rev}
		if err := s.Validate(); err == nil {
			t.Fatalf("Validate() with revision %d = nil, want error", rev)
		}
	}
}

func TestEncodeDecodeStateRoundTrips(t *testing.T) {
	want := State{Active: true, Kind: KindCancelNight, StartedAt: time.Now().UTC().Truncate(time.Millisecond), StartedBy: "lightning-feed", Revision: 42}
	data, err := EncodeState(want)
	if err != nil {
		t.Fatalf("EncodeState() = %v", err)
	}
	got, err := DecodeState(data)
	if err != nil {
		t.Fatalf("DecodeState() = %v", err)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	got.StartedAt = want.StartedAt
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestEncodeStateRejectsInvalid(t *testing.T) {
	if _, err := EncodeState(State{Active: true}); err == nil {
		t.Fatal("EncodeState() = nil error, want a validation error")
	}
}

func TestDecodeStateRejectsMalformed(t *testing.T) {
	if _, err := DecodeState([]byte(`not json`)); err == nil {
		t.Fatal("DecodeState() = nil error, want a decode error")
	}
}

func TestStartRequestValidate(t *testing.T) {
	r := StartRequest{Kind: KindDelay, IssuedAt: time.Now(), Nonce: "n1"}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestStartRequestValidateRejectsResumeLikeKind(t *testing.T) {
	r := StartRequest{Kind: "resume", IssuedAt: time.Now(), Nonce: "n1"}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error: there is no resume kind to express")
	}
}

func TestSignedStartRequestVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}
	req := StartRequest{Kind: KindDelay, IssuedAt: time.Now().UTC(), Nonce: "n1"}
	payload, err := req.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes() = %v", err)
	}
	sig := ed25519.Sign(priv, payload)
	sr := SignedStartRequest{Request: req, Signature: sig}
	if err := sr.Verify(pub); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestSignedStartRequestVerifyRejectsTamperedPayload(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}
	req := StartRequest{Kind: KindDelay, IssuedAt: time.Now().UTC(), Nonce: "n1"}
	payload, err := req.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes() = %v", err)
	}
	sig := ed25519.Sign(priv, payload)
	req.Nonce = "tampered"
	sr := SignedStartRequest{Request: req, Signature: sig}
	if err := sr.Verify(pub); err == nil {
		t.Fatal("Verify() = nil, want error for a tampered payload")
	}
}

func TestSignedStartRequestVerifyRejectsWrongKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}
	req := StartRequest{Kind: KindCancelNight, IssuedAt: time.Now().UTC(), Nonce: "n1"}
	payload, err := req.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes() = %v", err)
	}
	sig := ed25519.Sign(priv, payload)
	sr := SignedStartRequest{Request: req, Signature: sig}
	if err := sr.Verify(otherPub); err == nil {
		t.Fatal("Verify() = nil, want error for the wrong public key")
	}
}

type fixedSigner struct {
	priv ed25519.PrivateKey
}

func (s fixedSigner) Sign(payload []byte) (coordsig.Signature, error) {
	return coordsig.Signature(ed25519.Sign(s.priv, payload)), nil
}

func TestSignRoundTripsThroughVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}
	req := StartRequest{Kind: KindDelay, IssuedAt: time.Now().UTC(), Nonce: "n1"}
	sr, err := Sign(req, fixedSigner{priv: priv})
	if err != nil {
		t.Fatalf("Sign() = %v, want nil", err)
	}
	if err := sr.Verify(pub); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestSignRejectsInvalidRequest(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}
	if _, err := Sign(StartRequest{}, fixedSigner{priv: priv}); err == nil {
		t.Fatal("Sign() = nil, want error for an invalid request")
	}
}
