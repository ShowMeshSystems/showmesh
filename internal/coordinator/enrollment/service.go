package enrollment

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// The audit actions ADR-055 reserves.
const (
	AuditActionCreate = "node.enrollment.create"
	AuditActionCancel = "node.enrollment.cancel"
	AuditActionRedeem = "node.enrollment.redeem"
)

// Broker modes. Builtin is the default.
const (
	BrokerModeBuiltin  = "builtin"
	BrokerModeExternal = "external"
)

// Code states as the API reports them.
const (
	StatePending   = "pending"
	StateRedeemed  = "redeemed"
	StateExpired   = "expired"
	StateCancelled = "cancelled"
)

// Expiry bounds and default (ADR-055 decision 4).
const (
	MinExpiry     = time.Minute
	MaxExpiry     = 24 * time.Hour
	DefaultExpiry = 15 * time.Minute
)

// Errors a caller maps to a response. Each one's message is operator copy.
var (
	ErrInvalidNodeID     = errors.New("enrollment: invalid node id")
	ErrAlreadyEnrolled   = errors.New("enrollment: node already enrolled")
	ErrCodeNotFound      = errors.New("enrollment: code not found")
	ErrCodeNotPending    = errors.New("enrollment: code not pending")
	ErrMalformedCode     = errors.New("enrollment: malformed code")
	ErrCodeExpired       = errors.New("enrollment: code expired")
	ErrCodeUsed          = errors.New("enrollment: code already used")
	ErrCodeCancelled     = errors.New("enrollment: code cancelled")
	ErrPrincipalConflict = errors.New("enrollment: principal name taken")
	ErrRedeemFailed      = errors.New("enrollment: redeem failed")
)

// operatorError pairs a sentinel with the message an operator reads. cause
// is for the coordinator's log only and never reaches the caller.
type operatorError struct {
	kind  error
	msg   string
	cause error
}

func (e *operatorError) Error() string        { return e.msg }
func (e *operatorError) Is(target error) bool { return target == e.kind }

func opErr(kind error, format string, args ...any) error {
	return &operatorError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// Explain returns the message a caller may show for err and the cause the
// coordinator should log, which is nil when there is nothing more to say.
func Explain(err error) (message string, cause error) {
	var oe *operatorError
	if errors.As(err, &oe) {
		return oe.msg, oe.cause
	}
	return err.Error(), nil
}

// Config is the coordinator's enrollment settings, all read at start.
type Config struct {
	BrokerMode       string
	NodeBrokerURL    string
	PublicURL        string
	NodeMQTTUsername string
	NodeMQTTPassword string
	// CoordinatorPublicKey is the base64 of the coordinator's raw Ed25519
	// public key, the form the agent's key file holds.
	CoordinatorPublicKey string
}

// Service mints, lists, cancels and redeems enrollment codes.
type Service struct {
	st     *store.Store
	ids    identity.Service
	broker *BrokerFiles
	cfg    Config
	mu     sync.Mutex
}

// NewService returns a Service. broker is used only in builtin mode.
func NewService(st *store.Store, ids identity.Service, broker *BrokerFiles, cfg Config) *Service {
	if cfg.BrokerMode == "" {
		cfg.BrokerMode = BrokerModeBuiltin
	}
	return &Service{st: st, ids: ids, broker: broker, cfg: cfg}
}

// Code is one enrollment code as listed. It never carries the code.
type Code struct {
	ID         string
	NodeID     string
	Reenroll   bool
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	State      string
	RedeemedAt *time.Time
}

// Minted is a newly minted code, the only place the code itself appears.
type Minted struct {
	Code
	Value string
}

func codeFromRecord(rec store.NodeEnrollmentCodeRecord, now time.Time) Code {
	c := Code{
		ID: rec.ID, NodeID: rec.NodeID, Reenroll: rec.Reenroll, CreatedBy: rec.CreatedByName,
		CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt, RedeemedAt: rec.RedeemedAt,
	}
	if c.CreatedBy == "" {
		c.CreatedBy = rec.CreatedBy
	}
	switch {
	case rec.RedeemedAt != nil:
		c.State = StateRedeemed
	case rec.CancelledAt != nil:
		c.State = StateCancelled
	case !now.Before(rec.ExpiresAt):
		c.State = StateExpired
	default:
		c.State = StatePending
	}
	return c
}

// ValidateNodeID refuses a malformed node ID or one reserved for a fixed
// broker role.
func ValidateNodeID(nodeID string) error {
	if mqttproto.IsCommandNameNodeID(nodeID) {
		return opErr(ErrInvalidNodeID, "%q is reserved because showmeshctl node uses it as a command name. Choose a different node ID.", nodeID)
	}
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		return opErr(ErrInvalidNodeID, "%q is not a valid node ID. Use 1 to 64 lowercase letters, digits and inner hyphens.", nodeID)
	}
	if IsReservedNodeID(nodeID) {
		return opErr(ErrInvalidNodeID, "%q is reserved for the broker's own logins. Choose a different node ID.", nodeID)
	}
	return nil
}

// CheckCanMint reports why this coordinator cannot hand out broker logins,
// or nil.
func (s *Service) CheckCanMint() error {
	if s.cfg.BrokerMode != BrokerModeBuiltin {
		return nil
	}
	return s.broker.Check()
}

// enrolled reports whether nodeID has been enrolled before.
func (s *Service) enrolled(ctx context.Context, nodeID string) (bool, error) {
	_, found, err := s.st.LatestRedeemedNodeEnrollment(ctx, nodeID)
	if err != nil || found {
		return found, err
	}
	if s.cfg.BrokerMode != BrokerModeBuiltin {
		return false, nil
	}
	return s.broker.HasUser(nodeID)
}

// Mint creates a code for nodeID, cancelling that node's earlier unused
// code, and audits it against actor.
func (s *Service) Mint(ctx context.Context, actor identity.Authenticated, clientAddr, nodeID string, reenroll bool, expiresIn time.Duration, now time.Time) (Minted, error) {
	if err := ValidateNodeID(nodeID); err != nil {
		return Minted{}, err
	}
	if err := s.CheckCanMint(); err != nil {
		return Minted{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	isEnrolled, err := s.enrolled(ctx, nodeID)
	if err != nil {
		return Minted{}, err
	}
	if isEnrolled && !reenroll {
		return Minted{}, opErr(ErrAlreadyEnrolled, "Node %q is already enrolled. Mint a re-enrollment code to replace its credentials.", nodeID)
	}

	value, err := GenerateCode()
	if err != nil {
		return Minted{}, err
	}
	normalized, _ := NormalizeCode(value)
	rec := store.NodeEnrollmentCodeRecord{
		ID: uuid.NewString(), NodeID: nodeID, CodeHash: HashCode(normalized), Reenroll: reenroll,
		CreatedBy: actor.Principal.ID, CreatedByName: actor.Principal.Name,
		CreatedAt: now, ExpiresAt: now.Add(expiresIn),
	}
	err = s.ids.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		cancelled, err := tx.CancelPendingNodeEnrollmentCodes(ctx, nodeID, now)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if err := tx.InsertNodeEnrollmentCode(ctx, rec); err != nil {
			return identity.AuditEntry{}, err
		}
		params := map[string]any{"id": rec.ID, "reenroll": reenroll, "expiresAt": rec.ExpiresAt.UTC().Format(time.RFC3339)}
		if len(cancelled) > 0 {
			params["cancelledIds"] = cancelled
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: actor.Principal.ID, PrincipalName: actor.Principal.Name,
			Form: actor.Form, CredentialID: actor.CredentialID, ClientAddr: clientAddr,
			Action: AuditActionCreate, Target: nodeID, Params: params, Kind: identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		return Minted{}, err
	}
	return Minted{Code: codeFromRecord(rec, now), Value: value}, nil
}

// List returns every code, newest first.
func (s *Service) List(ctx context.Context, now time.Time) ([]Code, error) {
	recs, err := s.st.ListNodeEnrollmentCodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Code, 0, len(recs))
	for _, rec := range recs {
		out = append(out, codeFromRecord(rec, now))
	}
	return out, nil
}

// Cancel cancels pending code id and audits it against actor.
func (s *Service) Cancel(ctx context.Context, actor identity.Authenticated, clientAddr, id string, now time.Time) (Code, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Code
	err := s.ids.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		rec, err := tx.GetNodeEnrollmentCode(ctx, id)
		if errors.Is(err, store.ErrNodeEnrollmentCodeNotFound) {
			return identity.AuditEntry{}, opErr(ErrCodeNotFound, "No enrollment code has the id %q. List the codes to find the right one.", id)
		}
		if err != nil {
			return identity.AuditEntry{}, err
		}
		ok, err := tx.CancelNodeEnrollmentCode(ctx, id, now)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if !ok {
			state := codeFromRecord(rec, now).State
			return identity.AuditEntry{}, opErr(ErrCodeNotPending, "The enrollment code for node %q is already %s, so it cannot be cancelled.", rec.NodeID, state)
		}
		cancelledAt := now
		rec.CancelledAt = &cancelledAt
		result = codeFromRecord(rec, now)
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: actor.Principal.ID, PrincipalName: actor.Principal.Name,
			Form: actor.Form, CredentialID: actor.CredentialID, ClientAddr: clientAddr,
			Action: AuditActionCancel, Target: rec.NodeID, Params: map[string]any{"id": id}, Kind: identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		return Code{}, err
	}
	return result, nil
}

// RedeemRequest is one redeem call. RequestHost and RequestScheme are the
// host and scheme the request arrived on.
type RedeemRequest struct {
	Code          string
	Hostname      string
	Arch          string
	ClientAddr    string
	RequestHost   string
	RequestScheme string
}

// Redeemed is everything a node receives, once.
type Redeemed struct {
	NodeID               string
	BrokerURL            string
	MQTTUsername         string
	MQTTPassword         string
	APIToken             string
	CoordinatorURL       string
	CoordinatorPublicKey string
}

// PrincipalName is the machine principal a node's enrollment creates.
func PrincipalName(nodeID string) string { return nodeID + " agent" }

// Redeem exchanges a pending code for the node's credentials. On any
// failure it undoes what it did and the code stays pending.
func (s *Service) Redeem(ctx context.Context, req RedeemRequest, now time.Time) (Redeemed, error) {
	normalized, ok := NormalizeCode(req.Code)
	if !ok {
		return Redeemed{}, opErr(ErrMalformedCode, "The enrollment code must be 8 letters and digits, like ABCD-2345. Check the code and try again.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.st.GetNodeEnrollmentCodeByHash(ctx, HashCode(normalized))
	if errors.Is(err, store.ErrNodeEnrollmentCodeNotFound) {
		return Redeemed{}, opErr(ErrCodeNotFound, "This enrollment code is not known to the coordinator. Check the code, or mint a new one with showmeshctl node enroll.")
	}
	if err != nil {
		return Redeemed{}, err
	}
	switch codeFromRecord(rec, now).State {
	case StateRedeemed:
		return Redeemed{}, opErr(ErrCodeUsed, "This enrollment code has already been used. Mint a new one with showmeshctl node enroll --reenroll %s.", rec.NodeID)
	case StateCancelled:
		return Redeemed{}, opErr(ErrCodeCancelled, "This enrollment code was cancelled. Mint a new one with showmeshctl node enroll %s.", rec.NodeID)
	case StateExpired:
		return Redeemed{}, opErr(ErrCodeExpired, "This enrollment code expired at %s. Mint a new one with showmeshctl node enroll %s.", rec.ExpiresAt.UTC().Format(time.RFC3339), rec.NodeID)
	}
	nodeID := rec.NodeID
	previous, hasPrevious, err := s.st.LatestRedeemedNodeEnrollment(ctx, nodeID)
	if err != nil {
		return Redeemed{}, redeemFailed("read the node's earlier enrollment", err)
	}
	if !rec.Reenroll {
		if hasPrevious {
			return Redeemed{}, opErr(ErrAlreadyEnrolled, "Node %q was enrolled after this code was minted. Mint a re-enrollment code to replace its credentials.", nodeID)
		}
		if s.cfg.BrokerMode == BrokerModeBuiltin {
			hasLogin, err := s.broker.HasUser(nodeID)
			if err != nil {
				return Redeemed{}, redeemFailed("check whether the node already has a broker login", err)
			}
			if hasLogin {
				return Redeemed{}, opErr(ErrAlreadyEnrolled, "Node %q already has a broker login. Mint a re-enrollment code to replace its credentials.", nodeID)
			}
		}
	}
	reuseID := ""
	existing, err := s.st.GetPrincipalByName(ctx, PrincipalName(nodeID))
	switch {
	case err == nil && hasPrevious && existing.ID == previous.PrincipalID:
		reuseID = existing.ID
	case err == nil:
		return Redeemed{}, opErr(ErrPrincipalConflict, "A principal named %q already exists and was not created by enrollment. Rename it, then redeem the code again.", PrincipalName(nodeID))
	case !errors.Is(err, store.ErrPrincipalNotFound):
		return Redeemed{}, redeemFailed("look up the node's principal", err)
	}

	result := Redeemed{NodeID: nodeID, BrokerURL: s.brokerURL(req), CoordinatorURL: s.coordinatorURL(req), CoordinatorPublicKey: s.cfg.CoordinatorPublicKey}
	undoBroker := func() error { return nil }
	if s.cfg.BrokerMode == BrokerModeBuiltin {
		password, undo, err := s.broker.Provision(nodeID)
		if err != nil {
			return Redeemed{}, &operatorError{kind: ErrRedeemFailed, cause: err,
				msg: "The coordinator could not create the node's broker login, and the code is still valid. Check the coordinator's log, then try the code again."}
		}
		result.MQTTUsername, result.MQTTPassword, undoBroker = nodeID, password, undo
	} else {
		result.MQTTUsername, result.MQTTPassword = s.cfg.NodeMQTTUsername, s.cfg.NodeMQTTPassword
	}

	tok, err := identity.GenerateToken()
	if err != nil {
		return Redeemed{}, undoFailedOr(redeemFailed("generate the node's API token", err), err, undoBroker())
	}
	err = s.ids.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		principalID := reuseID
		var revoked int64
		if reuseID != "" {
			if err := tx.ResetEnrolledPrincipal(ctx, reuseID, string(identity.RoleNode)); err != nil {
				return identity.AuditEntry{}, err
			}
		} else {
			p, err := tx.CreatePrincipal(ctx, store.PrincipalRecord{
				ID: uuid.NewString(), Name: PrincipalName(nodeID), Kind: string(identity.KindMachine), Role: string(identity.RoleNode),
			})
			if err != nil {
				return identity.AuditEntry{}, err
			}
			principalID = p.ID
		}
		if hasPrevious && previous.PrincipalID != "" {
			n, err := tx.RevokeAllTokens(ctx, previous.PrincipalID)
			if err != nil {
				return identity.AuditEntry{}, err
			}
			revoked = n
		}
		if _, err := tx.CreateToken(ctx, store.TokenRecord{
			ID: uuid.NewString(), PrincipalID: principalID, Digest: tok.Digest, Hint: tok.Hint, Label: "node enrollment",
		}); err != nil {
			return identity.AuditEntry{}, err
		}
		marked, err := tx.MarkNodeEnrollmentCodeRedeemed(ctx, rec.ID, now, principalID, trimField(req.Hostname), trimField(req.Arch))
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if !marked {
			return identity.AuditEntry{}, opErr(ErrCodeUsed, "This enrollment code was used or cancelled a moment ago. Mint a new one with showmeshctl node enroll.")
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: rec.CreatedBy, PrincipalName: rec.CreatedByName,
			CredentialID: rec.ID, ClientAddr: req.ClientAddr,
			Action: AuditActionRedeem, Target: nodeID,
			Params: map[string]any{
				"id": rec.ID, "reenroll": rec.Reenroll, "principalId": principalID,
				"hostname": trimField(req.Hostname), "arch": trimField(req.Arch), "revokedTokens": revoked,
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		reported := err
		if !errors.Is(err, ErrCodeUsed) {
			reported = redeemFailed("record the enrollment", err)
		}
		return Redeemed{}, undoFailedOr(reported, err, undoBroker())
	}
	result.APIToken = tok.Value
	return result, nil
}

func redeemFailed(step string, err error) error {
	return &operatorError{kind: ErrRedeemFailed, cause: err,
		msg: fmt.Sprintf("The coordinator could not %s, and nothing was changed. Try the same code again.", step)}
}

// undoFailedOr returns reported, or a failure that says the broker login
// files could not be put back when undoErr is set.
func undoFailedOr(reported, err, undoErr error) error {
	if undoErr == nil {
		return reported
	}
	return &operatorError{kind: ErrRedeemFailed, cause: errors.Join(err, undoErr),
		msg: "The coordinator could not finish the enrollment or put the broker login files back. Check the broker config directory and the coordinator's log, then mint a new code."}
}

const maxFieldBytes = 128

// trimField bounds an optional, caller-supplied descriptive field: it keeps
// printable characters only and cuts on a UTF-8 boundary.
func trimField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if len(s) <= maxFieldBytes {
		return s
	}
	cut := maxFieldBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}

func (s *Service) brokerURL(req RedeemRequest) string {
	if s.cfg.NodeBrokerURL != "" {
		return s.cfg.NodeBrokerURL
	}
	return "tcp://" + net.JoinHostPort(requestHostname(req.RequestHost), "1883")
}

func (s *Service) coordinatorURL(req RedeemRequest) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	scheme := req.RequestScheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + req.RequestHost
}

// PublicURL is SHOWMESH_PUBLIC_URL without a trailing slash, or empty.
func (s *Service) PublicURL() string { return strings.TrimRight(s.cfg.PublicURL, "/") }

// requestHostname strips any port from an HTTP Host value.
func requestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}
