package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/enrollment"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// NodeEnrollmentService is ADR-055's enrollment code service, implemented
// by *enrollment.Service.
type NodeEnrollmentService interface {
	Mint(ctx context.Context, actor identity.Authenticated, clientAddr, nodeID string, reenroll bool, expiresIn time.Duration, now time.Time) (enrollment.Minted, error)
	List(ctx context.Context, now time.Time) ([]enrollment.Code, error)
	Cancel(ctx context.Context, actor identity.Authenticated, clientAddr, id string, now time.Time) (enrollment.Code, error)
	Redeem(ctx context.Context, req enrollment.RedeemRequest, now time.Time) (enrollment.Redeemed, error)
	PublicURL() string
}

var _ NodeEnrollmentService = (*enrollment.Service)(nil)

var scopeNodeEnroll = identity.ScopeNodeEnroll

const maxNodeEnrollmentBodyBytes = 4 * 1024

// Problem types for node enrollment.
const (
	ProblemTypeEnrollmentCodeGone        = problemBaseURI + "enrollment-code-gone"
	ProblemTypeNodeEnrollmentUnavailable = problemBaseURI + "node-enrollment-unavailable"
)

func enrollmentProblem(status int, typ, title, detail string) v1.Problem {
	return v1.Problem{Type: typ, Title: title, Status: status, Detail: detail}
}

// redeemLimiter bounds failed redeems: at most perSourceMax per
// perSourceWindow from one client address, and globalMax per globalWindow
// overall. A redeem holds a slot while it runs, so concurrent guesses
// cannot pass the check together; a successful redeem gives its slot back.
type redeemLimiter struct {
	mu        sync.Mutex
	perSource map[string][]*redeemSlot
	global    []*redeemSlot

	perSourceMax    int
	perSourceWindow time.Duration
	globalMax       int
	globalWindow    time.Duration
}

type redeemSlot struct{ at time.Time }

func newRedeemLimiter() *redeemLimiter {
	return &redeemLimiter{
		perSource: map[string][]*redeemSlot{}, perSourceMax: 5, perSourceWindow: time.Minute,
		globalMax: 30, globalWindow: time.Hour,
	}
}

func pruneBefore(slots []*redeemSlot, cutoff time.Time) []*redeemSlot {
	i := 0
	for i < len(slots) && !slots[i].at.After(cutoff) {
		i++
	}
	return slots[i:]
}

func withoutSlot(slots []*redeemSlot, slot *redeemSlot) []*redeemSlot {
	for i, s := range slots {
		if s == slot {
			return append(slots[:i:i], slots[i+1:]...)
		}
	}
	return slots
}

// reserve takes a failure slot for source. It returns how long source must
// wait when no slot is free, or a release func that gives the slot back.
func (l *redeemLimiter) reserve(source string, now time.Time) (time.Duration, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global = pruneBefore(l.global, now.Add(-l.globalWindow))
	src := pruneBefore(l.perSource[source], now.Add(-l.perSourceWindow))
	var wait time.Duration
	if len(src) >= l.perSourceMax {
		wait = src[len(src)-l.perSourceMax].at.Add(l.perSourceWindow).Sub(now)
	}
	if len(l.global) >= l.globalMax {
		if w := l.global[len(l.global)-l.globalMax].at.Add(l.globalWindow).Sub(now); w > wait {
			wait = w
		}
	}
	if len(src) >= l.perSourceMax || len(l.global) >= l.globalMax {
		if len(src) == 0 {
			delete(l.perSource, source)
		} else {
			l.perSource[source] = src
		}
		return max(wait, time.Second), nil
	}
	slot := &redeemSlot{at: now}
	l.perSource[source] = append(src, slot)
	l.global = append(l.global, slot)
	return 0, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.global = withoutSlot(l.global, slot)
		if rest := withoutSlot(l.perSource[source], slot); len(rest) == 0 {
			delete(l.perSource, source)
		} else {
			l.perSource[source] = rest
		}
	}
}

// isGuessOutcome reports whether a redeem failed because the code was
// wrong or no longer usable, the outcomes the failure limit counts.
func isGuessOutcome(err error) bool {
	return errors.Is(err, enrollment.ErrCodeNotFound) || errors.Is(err, enrollment.ErrCodeExpired) ||
		errors.Is(err, enrollment.ErrCodeUsed) || errors.Is(err, enrollment.ErrCodeCancelled)
}

func mapNodeEnrollment(c enrollment.Code) v1.NodeEnrollment {
	return v1.NodeEnrollment{
		ID: c.ID, NodeID: c.NodeID, Reenroll: c.Reenroll, CreatedBy: c.CreatedBy,
		CreatedAt: formatTime(c.CreatedAt), ExpiresAt: formatTime(c.ExpiresAt),
		State: c.State, RedeemedAt: formatTimePtr(c.RedeemedAt),
	}
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (h *handlers) nodeEnrollmentUnavailable(w http.ResponseWriter, now time.Time) bool {
	if h.deps.NodeEnrollment != nil {
		return false
	}
	writeProblem(w, h.logger, now, enrollmentProblem(http.StatusServiceUnavailable, ProblemTypeNodeEnrollmentUnavailable,
		"Node enrollment unavailable", "Node enrollment is not set up on this coordinator. Upgrade the coordinator, then try again."))
	return true
}

// writeEnrollmentError maps an enrollment error to its response. The
// response carries only operator copy; any underlying cause is logged.
func (h *handlers) writeEnrollmentError(w http.ResponseWriter, now time.Time, action string, err error) {
	detail, cause := enrollment.Explain(err)
	if cause != nil && h.logger != nil {
		h.logger.Error("api: node enrollment failed", "action", action, "error", cause)
	}
	var status int
	typ, title := ProblemTypeConflict, "Conflict"
	switch {
	case errors.Is(err, enrollment.ErrInvalidNodeID), errors.Is(err, enrollment.ErrMalformedCode):
		writeProblem(w, h.logger, now, invalidParameterProblem(detail))
		return
	case errors.Is(err, enrollment.ErrCodeNotFound):
		writeProblem(w, h.logger, now, resourceNotFoundProblem(detail))
		return
	case errors.Is(err, enrollment.ErrAlreadyEnrolled), errors.Is(err, enrollment.ErrCodeNotPending),
		errors.Is(err, enrollment.ErrPrincipalConflict):
		status = http.StatusConflict
	case errors.Is(err, enrollment.ErrCodeExpired), errors.Is(err, enrollment.ErrCodeUsed), errors.Is(err, enrollment.ErrCodeCancelled):
		status, typ, title = http.StatusGone, ProblemTypeEnrollmentCodeGone, "Enrollment code no longer valid"
	case errors.Is(err, enrollment.ErrBrokerFilesUnavailable), errors.Is(err, enrollment.ErrRedeemFailed):
		status, typ, title = http.StatusServiceUnavailable, ProblemTypeNodeEnrollmentUnavailable, "Node enrollment unavailable"
	default:
		h.writeInternalError(w, now, action, err)
		return
	}
	writeProblem(w, h.logger, now, enrollmentProblem(status, typ, title, detail))
}

func (h *handlers) handleCreateNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if h.nodeEnrollmentUnavailable(w, now) {
		return
	}
	var req v1.CreateNodeEnrollmentRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxNodeEnrollmentBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"The request body must be JSON like {\"nodeId\":\"render-01\",\"reenroll\":false,\"expiresInSeconds\":900}. Fix the body and try again."))
		return
	}
	expiresIn := enrollment.DefaultExpiry
	if req.ExpiresInSeconds != nil {
		expiresIn = time.Duration(*req.ExpiresInSeconds) * time.Second
		if expiresIn < enrollment.MinExpiry || expiresIn > enrollment.MaxExpiry {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				"expiresInSeconds must be between 60 and 86400. Choose an expiry in that range and try again."))
			return
		}
	}
	ac := authFromContext(r.Context())
	minted, err := h.deps.NodeEnrollment.Mint(r.Context(), ac.result, h.clientAddr(r), req.NodeID, req.Reenroll, expiresIn, now)
	if err != nil {
		h.writeEnrollmentError(w, now, "mint node enrollment code", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(v1.CreateNodeEnrollmentResponse{
		ServerTime: formatTime(now), ID: minted.ID, NodeID: minted.NodeID, Code: minted.Value,
		Reenroll: minted.Reenroll, ExpiresAt: formatTime(minted.ExpiresAt),
		CoordinatorURL: h.deps.NodeEnrollment.PublicURL(),
	})
}

func (h *handlers) handleListNodeEnrollments(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if h.nodeEnrollmentUnavailable(w, now) {
		return
	}
	codes, err := h.deps.NodeEnrollment.List(r.Context(), now)
	if err != nil {
		h.writeInternalError(w, now, "list node enrollment codes", err)
		return
	}
	out := v1.NodeEnrollmentsResponse{ServerTime: formatTime(now), Enrollments: make([]v1.NodeEnrollment, 0, len(codes))}
	for _, c := range codes {
		out.Enrollments = append(out.Enrollments, mapNodeEnrollment(c))
	}
	jsonWrite(w, out)
}

func (h *handlers) handleCancelNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if h.nodeEnrollmentUnavailable(w, now) {
		return
	}
	ac := authFromContext(r.Context())
	code, err := h.deps.NodeEnrollment.Cancel(r.Context(), ac.result, h.clientAddr(r), r.PathValue("id"), now)
	if err != nil {
		h.writeEnrollmentError(w, now, "cancel node enrollment code", err)
		return
	}
	jsonWrite(w, v1.NodeEnrollmentResponse{ServerTime: formatTime(now), Enrollment: mapNodeEnrollment(code)})
}

// handleRedeemNodeEnrollment serves POST /api/v1/node-enrollments/redeem.
// It takes no principal and no same-origin check: the code is the
// credential. The failure limit is what makes a short code safe.
func (h *handlers) handleRedeemNodeEnrollment(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if h.nodeEnrollmentUnavailable(w, now) {
		return
	}
	source := loginSource(r)
	wait, release := h.redeemLimiter.reserve(source, now)
	if release == nil {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
		writeProblem(w, h.logger, now, tooManyRequestsProblem(
			"Too many enrollment codes were refused recently. Wait "+strconv.Itoa(retryAfterSeconds(wait))+" seconds, then try again."))
		return
	}
	var req v1.RedeemNodeEnrollmentRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxNodeEnrollmentBodyBytes))
	if err := dec.Decode(&req); err != nil {
		release()
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"The request body must be JSON like {\"code\":\"ABCD-2345\"}. Fix the body and try again."))
		return
	}
	res, err := h.deps.NodeEnrollment.Redeem(r.Context(), enrollment.RedeemRequest{
		Code: req.Code, Hostname: req.Hostname, Arch: req.Arch, ClientAddr: h.clientAddr(r),
		RequestHost: r.Host, RequestScheme: requestScheme(r),
	}, now)
	if !isGuessOutcome(err) {
		release()
	}
	if err != nil {
		h.writeEnrollmentError(w, now, "redeem node enrollment code", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonWrite(w, v1.RedeemNodeEnrollmentResponse{
		ServerTime: formatTime(now), NodeID: res.NodeID, BrokerURL: res.BrokerURL,
		MQTTUsername: res.MQTTUsername, MQTTPassword: res.MQTTPassword, APIToken: res.APIToken,
		CoordinatorURL: res.CoordinatorURL, CoordinatorPublicKey: res.CoordinatorPublicKey,
	})
}
