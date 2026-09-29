package v1

// FPPPairingRequest is the body of POST /api/v1/fpp/{instanceId}/pairing:
// the pairing code an operator read off the FPP plugin's own page.
type FPPPairingRequest struct {
	// Code is the eight-character code in XXXX-XXXX form. The
	// coordinator derives the same code from the secret the plugin
	// presents later, so a code alone never proves anything.
	Code string `json:"code"`
}

// FPPPairingResponse is the 200 body of POST
// /api/v1/fpp/{instanceId}/pairing. The minted token is deliberately
// absent: only the plugin that holds the secret ever receives it.
type FPPPairingResponse struct {
	ServerTime  string `json:"serverTime"`
	InstanceID  string `json:"instanceId"`
	Code        string `json:"code"`
	PrincipalID string `json:"principalId"`
	State       string `json:"state"`
	ExpiresAt   string `json:"expiresAt"`
}

// FPPPairingStateResponse is the 200 body of GET
// /api/v1/fpp/{instanceId}/pairing: "none", "waiting", or "paired".
type FPPPairingStateResponse struct {
	ServerTime  string  `json:"serverTime"`
	State       string  `json:"state"`
	Code        string  `json:"code"`
	ExpiresAt   *string `json:"expiresAt"`
	PairedAt    *string `json:"pairedAt"`
	PrincipalID string  `json:"principalId"`
}

// FPPPairingClaimRequest is the body of the unauthenticated
// POST /api/v1/integrations/fpp/pairing/claim.
type FPPPairingClaimRequest struct {
	// Secret is 32 random bytes as 64 lower-case hex characters. It is
	// never logged, never audited, and never returned.
	Secret string `json:"secret"`
}

// FPPPairingClaimResponse is the 200 body of the claim route: the only
// time the minted token appears on the wire.
type FPPPairingClaimResponse struct {
	Token       string `json:"token"`
	PrincipalID string `json:"principalId"`
	InstanceID  string `json:"instanceId"`
}
