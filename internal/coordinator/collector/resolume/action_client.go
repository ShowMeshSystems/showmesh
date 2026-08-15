package resolume

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// D-3's extension of [Client]: the write calls TRACK-D-D3-SPEC.md §2's table
// names, plus the one targeted GET (Column) launchColumn's confirming
// predicate needs. Kept out of client.go so that file's own claim — no method
// there can send a POST, PUT or DELETE — stays true of that file.
//
// Every wire shape below comes from Arena's own OpenAPI specification
// (docs/bench/resolume-control-surface.md section 17), which states method,
// path and body schema but cannot say whether Arena's server enforces that
// schema strictly, nor which of several schema-legal bodies produces which
// effect on the wall. Two changes here are spec-conformant but NOT re-verified
// against a live Arena, and each says so at its own site: omitting a connect
// body ([Client.ConnectClip]) and adding "valuetype" to a parameter PUT
// ([parameterPutBody]).

// maxActionResponseBytes bounds a write dispatch's response body: a 204 or a
// short plain-text error, never large. Exists so a misbehaving Resolume
// streaming an unbounded body cannot exhaust coordinator memory.
const maxActionResponseBytes = 4 << 10 // 4 KiB

// --- connect: no body constant here, deliberately ---------------------------
//
// There is no function or constant anywhere in this file that can produce
// a `false` connect body, and none that produces `true` either: TRACK-D-D3-SPEC.md
// §3.1's own warning is that "a builder who
// adds the off switch to launchClip has added nothing and reported
// success," because `false` is measured to return 204 and do nothing
// (capture §2.6) — but the vendor's own specification, verbatim, on both
// the clip and column connect operations: "This is analogous to whether
// the mouse is pressed down on the clip. If omitted, true and false are
// both sent — as if a short click was generated." The vendor's own
// documented COMPLETE gesture is omitting the body, not sending `true`.
// Sending a literal `true` was MEASURED to work on the operator's own
// composition (capture §7); omitting the body is the vendor-documented
// click and has NOT been re-measured against a live Arena, and the
// difference between the two has not been measured at all on a clip whose
// own `triggerstyle` is momentary, where a held mouse-down that never
// releases is plausibly different from a click. Structurally impossible to
// send `false`, exactly as the previous fixed-`true` body was — the
// identical judgment call [ParameterID.MarshalJSON] already makes for a
// different rule: the mistake this prevents is cheaper to make impossible
// than to remember not to make.

// parameterPutBody is the JSON body [Client.SetParameterBool] and
// [Client.SetParameterRange] send to `PUT /parameter/by-id/{id}`. The
// request schema is a `oneOf` over seven parameter schemas (capture §17),
// and `TextParameter.value` carries no type constraint of its own, so
// `{"value": true}` alone is schema-legal against BOTH `TextParameter` and
// `BooleanParameter` — an ambiguity a strict `oneOf` validator rejects
// outright (a `400` is declared on this operation). Valuetype selects
// exactly one branch and costs nothing: "ParamBoolean" for
// [Client.SetParameterBool], "ParamRange" for [Client.SetParameterRange].
// Deliberately NOT echoing a full parameter object including "id":
// [ParameterID.MarshalJSON] structurally refuses to serialize one, and id
// is redundant with the path this body is PUT to anyway. Whether Arena's
// own server is actually strict about `oneOf` is not knowable from a
// specification — this is a spec-conformant change, not a measured one.
type parameterPutBody struct {
	Valuetype string `json:"valuetype"`
	Value     any    `json:"value"`
}

// Column is `GET /composition/columns/by-id/{id}`'s targeted decode.
// Connected is a column's OWN three-state value (Empty|Disconnected|
// Connected, capture §4.3) — a different option set from a clip's five-state
// connected, decoded through the same [ParamStateField] because both are
// ParamState leaves on the wire.
type Column struct {
	ID        ObjectID         `json:"id"`
	Connected ParamStateField  `json:"connected"`
	Name      ParamStringField `json:"name"`
}

// Column performs GET /composition/columns/by-id/{id}. Not part of D-2:
// nothing before Track D seam D-3 had a reason to read a column at all.
func (c *Client) Column(ctx context.Context, id ObjectID) (Column, error) {
	return getByID[Column](ctx, c, "/composition/columns/by-id/"+id.String())
}

// ConnectClip performs POST /composition/clips/by-id/{id}/connect with NO
// body — see the "connect: no body constant here" section above for why.
func (c *Client) ConnectClip(ctx context.Context, id ObjectID) error {
	return c.doWrite(ctx, http.MethodPost, "/composition/clips/by-id/"+id.String()+"/connect", nil)
}

// ConnectColumn performs POST /composition/columns/by-id/{id}/connect with
// NO body. See [Client.ConnectClip]'s own doc comment.
func (c *Client) ConnectColumn(ctx context.Context, id ObjectID) error {
	return c.doWrite(ctx, http.MethodPost, "/composition/columns/by-id/"+id.String()+"/connect", nil)
}

// ClearLayer performs POST /composition/layers/by-id/{id}/clear — one of
// the two verified disconnect operations (capture §2.6's own table; the
// other is [Client.DisconnectAll]). No body: this is a pure trigger, not a
// mouse-down/mouse-up toggle the way connect is.
func (c *Client) ClearLayer(ctx context.Context, id ObjectID) error {
	return c.doWrite(ctx, http.MethodPost, "/composition/layers/by-id/"+id.String()+"/clear", nil)
}

// DisconnectAll performs POST /composition/disconnect-all — capture §2.6's
// "stop everything." No body, no object id: this call is not addressed to
// any single object.
func (c *Client) DisconnectAll(ctx context.Context) error {
	return c.doWrite(ctx, http.MethodPost, "/composition/disconnect-all", nil)
}

// SelectDeck performs POST /composition/decks/by-id/{id}/select. No body:
// unlike connect, "select" has no measured or documented mouse-down/
// mouse-up toggle semantics to worry about — it is a pure trigger.
func (c *Client) SelectDeck(ctx context.Context, id ObjectID) error {
	return c.doWrite(ctx, http.MethodPost, "/composition/decks/by-id/"+id.String()+"/select", nil)
}

// SetParameterBool performs PUT /parameter/by-id/{id} with body
// {"valuetype": "ParamBoolean", "value": value} — setLayerBypass's dispatch
// call. id is a LIVE, session-scoped [ParameterID] the caller must have just
// read, never a persisted one. See [parameterPutBody] on valuetype.
func (c *Client) SetParameterBool(ctx context.Context, id ParameterID, value bool) error {
	body, err := json.Marshal(parameterPutBody{Valuetype: "ParamBoolean", Value: value})
	if err != nil {
		return fmt.Errorf("resolume: encoding parameter PUT body: %w", err)
	}
	return c.doWrite(ctx, http.MethodPut, "/parameter/by-id/"+id.String(), body)
}

// SetParameterRange performs PUT /parameter/by-id/{id} with body
// {"valuetype": "ParamRange", "value": value} — setLayerMaster's own
// dispatch call. See [Client.SetParameterBool]'s own doc comment on id's
// lifecycle and [parameterPutBody]'s own doc comment on valuetype.
func (c *Client) SetParameterRange(ctx context.Context, id ParameterID, value float64) error {
	body, err := json.Marshal(parameterPutBody{Valuetype: "ParamRange", Value: value})
	if err != nil {
		return fmt.Errorf("resolume: encoding parameter PUT body: %w", err)
	}
	return c.doWrite(ctx, http.MethodPut, "/parameter/by-id/"+id.String(), body)
}

// doWrite issues one bounded POST or PUT against c.baseURL+apiPrefix+path.
// A non-2xx becomes a [StatusError]; a 2xx body is discarded unread, because
// a write response is never evidence of anything (§3.2: "a 204 is an
// acknowledgement, never evidence"). Nothing in action_dispatch.go may read
// this return value as more than "did the request itself succeed or fail".
func (c *Client) doWrite(ctx context.Context, method, path string, body []byte) error {
	reqCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+apiPrefix+path, reader)
	if err != nil {
		return fmt.Errorf("resolume: building %s request for %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newStatusError(resp, path)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxActionResponseBytes))
	return nil
}
