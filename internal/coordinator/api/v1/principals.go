package v1

// Wire types for Track G seam G-5: identity administration over the API
// (principals and their tokens). Distinct from PrincipalSummary/SessionInfo
// in types.go, which are GET /api/v1/session's own narrower "who am I"
// shape — this file is the admin surface's own full object.

// PrincipalObject is one principal as this admin surface renders it.
// HasPassword and Reserved are booleans, never the password hash or any
// other secret. CreatedAt is RFC 3339 (formatTime).
type PrincipalObject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	HasPassword bool   `json:"hasPassword"`
	Reserved    bool   `json:"reserved"`
	CreatedAt   string `json:"createdAt"`
}

// PrincipalsResponse is the body of GET /api/v1/principals.
type PrincipalsResponse struct {
	ServerTime string            `json:"serverTime"`
	Principals []PrincipalObject `json:"principals"`
}

// PrincipalResponse is the body of GET /api/v1/principals/{id}, POST
// /api/v1/principals, PUT .../role, POST .../enable, .../disable, and
// .../password.
type PrincipalResponse struct {
	ServerTime string          `json:"serverTime"`
	Principal  PrincipalObject `json:"principal"`
}

// CreatePrincipalRequest is POST /api/v1/principals' body. Password may be
// absent/null/empty — all three mean "no password, token-only", matching
// [identity.Service.CreatePrincipal]'s own existing tolerance (a machine
// principal that will only ever use an issued token). Name, Kind, and Role
// are required.
type CreatePrincipalRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

// SetPrincipalRoleRequest is PUT /api/v1/principals/{id}/role's body.
type SetPrincipalRoleRequest struct {
	Role string `json:"role"`
}

// SetPrincipalPasswordRequest is POST /api/v1/principals/{id}/password's
// body. Unlike CreatePrincipalRequest.Password, this one must be non-empty
// — a reset that silently clears a password would leave a human principal
// with no way to sign in at all.
type SetPrincipalPasswordRequest struct {
	Password string `json:"password"`
}

// TokenObject is one API token's non-secret metadata on the wire — never
// a digest or a raw value, matching [identity.TokenInfo]'s own guarantee.
type TokenObject struct {
	ID          string  `json:"id"`
	PrincipalID string  `json:"principalId"`
	Hint        string  `json:"hint"`
	Label       string  `json:"label"`
	CreatedAt   string  `json:"createdAt"`
	ExpiresAt   *string `json:"expiresAt"`
	LastUsedAt  *string `json:"lastUsedAt"`
}

// TokensResponse is the body of GET /api/v1/principals/{id}/tokens.
type TokensResponse struct {
	ServerTime string        `json:"serverTime"`
	Tokens     []TokenObject `json:"tokens"`
}

// IssueTokenRequest is POST /api/v1/principals/{id}/tokens' body. ExpiresAt
// is a pointer so absent and explicit null both mean "never expires"
// (ADR-024 decision 1's default) — the same meaning either way, so there is
// no absent-vs-null ambiguity for this field to resolve.
type IssueTokenRequest struct {
	Label     string  `json:"label"`
	ExpiresAt *string `json:"expiresAt"`
}

// IssueTokenResponse is POST /api/v1/principals/{id}/tokens' response.
// Value carries the token's plaintext exactly once (ADR-024 decision 1) —
// no other endpoint in this surface ever renders it again.
type IssueTokenResponse struct {
	ServerTime string      `json:"serverTime"`
	Token      TokenObject `json:"token"`
	Value      string      `json:"value"`
}
