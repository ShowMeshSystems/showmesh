package coordinator

// This file is the one regression test ADR-039 decision 7 exists to
// protect and that a rewrite is most likely to lose: GET reports the
// credential's LIVE presence in the credentials table, never a
// config_revisions row's own stored passwordSet marker. See
// api/fppmqttconfig.go's handleGetFPPMQTTConfig doc comment and
// internal/coordinator/store/credentials.go's HasCredential doc comment for
// the property this test pins.

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestFPPMQTTSecretAdapterHasPasswordIgnoresRevisionClaimFailsIfSimplifiedToTrustRevision
// is named for its own failure mode: a config_revisions row claims
// passwordSet=true (EncodeFPPMQTTPayload(cfg, true)), but the credentials
// table holds nothing for fpp.mqtt's password field. If a future change
// "simplifies" HasFPPMQTTPassword into decoding the active revision's own
// marker instead of querying the credentials table live, this test fails
// with passwordSet=true when the honest answer is false.
func TestFPPMQTTSecretAdapterHasPasswordIgnoresRevisionClaimFailsIfSimplifiedToTrustRevision(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	payload, err := config.EncodeFPPMQTTPayload(config.FPPMQTTConfig{BrokerURL: "tcp://10.0.1.5:1883"}, true)
	if err != nil {
		t.Fatalf("EncodeFPPMQTTPayload: %v", err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.FPPMQTTConfigKind, ObjectID: config.FPPMQTTConfigObjectID,
		Revision: 1, PayloadJSON: payload, Source: config.FPPMQTTSourceAPI,
	}); err != nil {
		t.Fatalf("CreateConfigRevision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, 1); err != nil {
		t.Fatalf("ActivateConfigRevision: %v", err)
	}

	// Sanity check: the revision really does claim a password is set, the
	// condition this test exists to be misled by if HasFPPMQTTPassword ever
	// trusts it.
	_, claimedSet, err := config.DecodeFPPMQTTPayload(payload)
	if err != nil {
		t.Fatalf("DecodeFPPMQTTPayload: %v", err)
	}
	if !claimedSet {
		t.Fatalf("test setup bug: the seeded revision must claim passwordSet=true")
	}

	adapter := fppMQTTSecretAdapter{st: st}
	present, err := adapter.HasFPPMQTTPassword(ctx)
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword: %v", err)
	}
	if present {
		t.Fatalf("HasFPPMQTTPassword = true, want false: no credential was ever written, only a revision that claims one; " +
			"GET must read the credentials table live, never the revision's own marker")
	}

	// Now write the credential for real and confirm presence flips to true,
	// proving the false result above was a live "not set" answer, not a
	// broken query that always returns false.
	if err := st.SetCredential(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, config.FPPMQTTPasswordCredentialField, "s3cret"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	present, err = adapter.HasFPPMQTTPassword(ctx)
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword after SetCredential: %v", err)
	}
	if !present {
		t.Fatalf("HasFPPMQTTPassword = false after SetCredential, want true")
	}

	// And clearing the credential while the revision STILL claims
	// passwordSet=true must again report false live, the exact disagreement
	// window this design exists to answer honestly.
	if err := st.ClearCredential(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, config.FPPMQTTPasswordCredentialField); err != nil {
		t.Fatalf("ClearCredential: %v", err)
	}
	present, err = adapter.HasFPPMQTTPassword(ctx)
	if err != nil {
		t.Fatalf("HasFPPMQTTPassword after ClearCredential: %v", err)
	}
	if present {
		t.Fatalf("HasFPPMQTTPassword = true after ClearCredential, want false: the revision still claims passwordSet=true " +
			"but the credential is genuinely gone, and GET must never lie in the revision's favor")
	}
}
