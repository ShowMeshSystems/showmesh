package coordsig

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

func TestVerify_CorrectSignatureVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	payload := []byte("fallback program revision abc123")
	sig := Signature(ed25519.Sign(priv, payload))

	if err := sig.Verify(payload, pub); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestVerify_TamperedPayloadFails(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sig := Signature(ed25519.Sign(priv, []byte("original payload")))

	err = sig.Verify([]byte("tampered payload"), pub)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("Verify() = %v, want ErrSignatureInvalid", err)
	}
}

func TestVerify_WrongKeyFails(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	payload := []byte("some payload")
	sig := Signature(ed25519.Sign(priv, payload))

	err = sig.Verify(payload, otherPub)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("Verify() = %v, want ErrSignatureInvalid", err)
	}
}

func TestVerify_CorruptPublicKeyReportsErrorNotPanic(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	payload := []byte("some payload")
	sig := Signature(ed25519.Sign(priv, payload))

	shortKey := ed25519.PublicKey([]byte{1, 2, 3})

	err = sig.Verify(payload, shortKey)
	if !errors.Is(err, ErrKeySize) {
		t.Fatalf("Verify() = %v, want ErrKeySize", err)
	}
}

func TestVerify_CorruptSignatureReportsErrorNotPanic(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	payload := []byte("some payload")
	shortSig := Signature([]byte{9, 9, 9})

	err = shortSig.Verify(payload, pub)
	if !errors.Is(err, ErrSignatureSize) {
		t.Fatalf("Verify() = %v, want ErrSignatureSize", err)
	}
}

func TestVerify_MissingKeyMaterialReportsErrorNotPanic(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	payload := []byte("some payload")
	sig := Signature(ed25519.Sign(priv, payload))

	err = sig.Verify(payload, nil)
	if !errors.Is(err, ErrKeySize) {
		t.Fatalf("Verify() = %v, want ErrKeySize", err)
	}

	err = Signature(nil).Verify(payload, priv.Public().(ed25519.PublicKey))
	if !errors.Is(err, ErrSignatureSize) {
		t.Fatalf("Verify() with nil signature = %v, want ErrSignatureSize", err)
	}
}
