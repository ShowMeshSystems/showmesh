package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	gatewayRequestTimeout  = 5 * time.Second
	gatewaySnapshotTimeout = 30 * time.Second
	gatewayWebRTCTimeout   = 15 * time.Second
	maxGatewayBodyBytes    = 8 << 20
)

// Source is one NDI source the gateway currently sees announced.
type Source struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Stream is one entry of the desired stream set. NDIName is set for kind "ndi",
// URL for kind "rtsp".
type Stream struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	NDIName string `json:"ndiName,omitempty"`
	URL     string `json:"url,omitempty"`
}

// StreamState is the gateway's report on one stream.
type StreamState struct {
	Key       string `json:"key"`
	State     string `json:"state"`
	Message   string `json:"message"`
	Consumers int    `json:"consumers"`
}

// GatewayError is a reply from the gateway that was not a success.
type GatewayError struct {
	Status  int
	Message string
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("preview gateway answered %d: %s", e.Status, e.Message)
}

// ErrGatewayTimeout means the gateway was reachable but a slow call did not finish in time.
var ErrGatewayTimeout = errors.New("preview gateway call timed out")

// Client talks to the gateway's own API. The token is sent on every request.
type Client struct {
	base  string
	token string
	http  *http.Client

	snapshotTimeout time.Duration
	webrtcTimeout   time.Duration
}

// NewClient returns a client for the gateway at baseURL.
func NewClient(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	hc = &noRedirect
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: hc,
		snapshotTimeout: gatewaySnapshotTimeout, webrtcTimeout: gatewayWebRTCTimeout}
}

func (c *Client) do(ctx context.Context, timeout time.Duration, method, path, contentType string, body []byte) ([]byte, http.Header, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, hdr, err := c.send(ctx, method, path, contentType, body)
	if err != nil && parent.Err() == nil && isTimeout(err) {
		return nil, nil, ErrGatewayTimeout
	}
	return data, hdr, err
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func (c *Client) send(ctx context.Context, method, path, contentType string, body []byte) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGatewayBodyBytes))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, &GatewayError{Status: resp.StatusCode, Message: strings.TrimSpace(string(truncate(data, 200)))}
	}
	return data, resp.Header, nil
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// Sources lists the NDI sources the gateway sees.
func (c *Client) Sources(ctx context.Context) ([]Source, error) {
	data, _, err := c.do(ctx, gatewayRequestTimeout, http.MethodGet, "/v1/sources", "", nil)
	if err != nil {
		return nil, err
	}
	var out []Source
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("preview gateway sources reply: %w", err)
	}
	return out, nil
}

// PutStreams replaces the gateway's desired stream set.
func (c *Client) PutStreams(ctx context.Context, streams []Stream) error {
	if streams == nil {
		streams = []Stream{}
	}
	b, err := json.Marshal(streams)
	if err != nil {
		return err
	}
	_, _, err = c.do(ctx, gatewayRequestTimeout, http.MethodPut, "/v1/streams", "application/json", b)
	return err
}

// Streams reports the state of every stream in the desired set.
func (c *Client) Streams(ctx context.Context) ([]StreamState, error) {
	data, _, err := c.do(ctx, gatewayRequestTimeout, http.MethodGet, "/v1/streams", "", nil)
	if err != nil {
		return nil, err
	}
	var out []StreamState
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("preview gateway streams reply: %w", err)
	}
	return out, nil
}

// Offer sends a browser's SDP offer for one stream and returns the SDP answer.
func (c *Client) Offer(ctx context.Context, key string, offer []byte) ([]byte, error) {
	data, _, err := c.do(ctx, c.webrtcTimeout, http.MethodPost, "/v1/streams/"+url.PathEscape(key)+"/webrtc", "application/sdp", offer)
	return data, err
}

// Snapshot returns the latest JPEG frame of one stream.
func (c *Client) Snapshot(ctx context.Context, key string) ([]byte, error) {
	data, _, err := c.do(ctx, c.snapshotTimeout, http.MethodGet, "/v1/streams/"+url.PathEscape(key)+"/snapshot", "", nil)
	return data, err
}
