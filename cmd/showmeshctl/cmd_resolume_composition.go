package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// This file is Track D seam D-2a's showmeshctl surface: `resolume
// composition upload` and `resolume composition show`, over
// POST/GET /api/v1/config/resolume/composition. Per this program's own
// independence rule (doc.go, importgraph_test.go), every wire type below
// is this file's own transcription of the contract, not a shared struct
// with the coordinator's internal types.
//
// This is the FIRST upload this CLI has ever issued, and the first
// non-JSON request body: every prior write (config set, discover,
// declare, undeclare, the eight "fpp <verb>" commands) sends
// application/json. A composition file is opened, validated, and read
// entirely client-side before any request is attempted — see
// cmdResolumeCompositionUpload — so a missing or unreadable path is a
// usage error, never a transport failure the server had any chance to
// see.
//
// ADR-032 decision 7 governs `upload`'s own output: the coordinator's
// parse result is rendered as what was found (composition name, Arena
// version, canvas size, deck names with clip counts, layer/clip/
// persistent counts) so an operator can recognise their own show, never
// as a bare success indicator. ADR-032 decision 6 governs `show`: a
// Resolume clip id resolves over the live API only while its own deck is
// selected (30/30 measured against 0/10 for other decks), so this program
// never prints a clip without the deck it belongs to.

// resolumeArenaVersion is the Arena build that wrote a composition file
// (the "writtenBy" object on the wire).
type resolumeArenaVersion struct {
	Product  string `json:"product"`
	Major    int    `json:"major"`
	Minor    int    `json:"minor"`
	Micro    int    `json:"micro"`
	Revision int    `json:"revision"`
}

// String renders the Arena version the way an operator would recognise it
// from Resolume's own "About" dialog, not as four separate fields.
func (v resolumeArenaVersion) String() string {
	return fmt.Sprintf("%s %d.%d.%d (build %d)", v.Product, v.Major, v.Minor, v.Micro, v.Revision)
}

// resolumeCanvas is composition.canvas: the composition's pixel dimensions.
type resolumeCanvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// resolumeDeckSummary is one element of composition.decks (and of the
// stored id map's own top-level "decks"): a deck's name, whether it is
// closed, and how many clips it holds, not the clips themselves.
type resolumeDeckSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Closed    bool   `json:"closed"`
	ClipCount int    `json:"clipCount"`
}

// resolumeCompositionSummary is the "composition" object common to both
// the upload response and the stored-composition read: what the
// coordinator parsed, in terms an operator recognises (ADR-032 decision
// 7), never a bare success flag.
//
// Review finding A: an earlier version of this type was missing
// sizeBytes and columnCount, two fields the wire contract
// (ResolumeCompositionSummary, internal/coordinator/api/v1/types.go)
// actually sends. Both are decoded here now — the CLI is the "the show is
// broken and the UI is down" path (ADR-030), and it must not silently
// discard fields the operator might need to confirm they uploaded the
// right file.
type resolumeCompositionSummary struct {
	Name                string                `json:"name"`
	SourceFilename      string                `json:"sourceFilename"`
	ContentHash         string                `json:"contentHash"`
	SizeBytes           int64                 `json:"sizeBytes"`
	WrittenBy           resolumeArenaVersion  `json:"writtenBy"`
	Canvas              resolumeCanvas        `json:"canvas"`
	Decks               []resolumeDeckSummary `json:"decks"`
	LayerCount          int                   `json:"layerCount"`
	LayerGroupCount     int                   `json:"layerGroupCount"`
	ColumnCount         int                   `json:"columnCount"`
	ClipCount           int                   `json:"clipCount"`
	PersistentClipCount int                   `json:"persistentClipCount"`
}

// resolumeCompositionUploadResponse is the body of a successful
// POST /api/v1/config/resolume/composition. ServerTime is a pointer,
// decoded tolerantly rather than required: the wire contract's own
// illustrated example for this endpoint does not show a "serverTime" key
// the way every other response in this program's contract does (contract
// §6.2: "every response body carries serverTime"), so this program
// assumes nothing about whether the field is actually sent and only acts
// on it (the clock-skew warning) when it is present, per this program's
// own additive-only, tolerate-an-unexpected-shape posture (types.go's own
// doc comment) applied to the opposite case — a field that MIGHT be
// missing rather than one that might be new.
type resolumeCompositionUploadResponse struct {
	ServerTime  *time.Time                 `json:"serverTime"`
	Revision    int64                      `json:"revision"`
	ActivatedAt time.Time                  `json:"activatedAt"`
	Composition resolumeCompositionSummary `json:"composition"`
}

// resolumeLayerGroup is one element of the stored id map's layerGroups.
//
// Review finding A: an earlier version of this type declared {id, name}.
// The coordinator's own wire type (ResolumeCompositionLayerGroup,
// internal/coordinator/api/v1/types.go) sends {id, index} — there is no
// "name" on a layer group anywhere in the .avc format (pkg/resolumecomp's
// LayerGroup carries no Name field either) — so decoding into a "name"
// field silently produced an empty string this program then printed,
// while Index (the position ResolumeCompositionLayer.LayerGroupIndex
// actually refers to) was dropped on the floor entirely.
type resolumeLayerGroup struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// resolumeLayer is one element of the stored id map's layers. Layer ids
// are deck-independent (ADR-032 decision 6: only clip ids need their own
// deck resolved before selection), so this type carries no deck
// reference.
//
// Review finding A: an earlier version declared {id, name} — the same
// invented-field mistake as [resolumeLayerGroup]. That was wrong at the
// time (a layer's own name attribute is a literal "Layer", never the
// operator's), but ADR-037 decision 7 made "name" real: the server now
// reads a layer's actual name from the composition file's own nested
// Param and sends it here as "name", never blank — see Name and
// NameGenerated below. layerGroupIndex is exactly half of the
// "structural id map" ADR-032 decision 1 exists to store: which group a
// layer belongs to. LayerGroupIndex is a pointer, matching the server's
// own [omitempty] rule (internal/coordinator/api/v1/types.go's
// ResolumeCompositionLayer): absent, not sent as null, when the
// composition has no layer groups at all.
type resolumeLayer struct {
	ID              string `json:"id"`
	Index           int    `json:"index"`
	LayerGroupIndex *int   `json:"layerGroupIndex,omitempty"`

	// Name is the layer's own display name: the operator's own value
	// when the composition file carried one, or a coordinator-generated
	// positional label ("Layer <n>") when it did not (ADR-037 decisions 4
	// and 7). Always present and never blank — NameGenerated says which
	// case this is, so this program can mark a generated label as such
	// rather than passing it off as something the operator typed.
	Name          string `json:"name"`
	NameGenerated bool   `json:"nameGenerated"`
}

// resolumeColumn is one element of the stored id map's columns.
//
// Review finding A: an earlier version declared {id, name} — the same
// invented-field mistake as [resolumeLayerGroup] and [resolumeLayer].
// The server sends {id, deckId, index}: a column belongs to exactly one
// deck (unlike a layer, which is deck-independent), and DeckID is the
// other structural relation this program used to silently drop.
type resolumeColumn struct {
	ID     string `json:"id"`
	DeckID string `json:"deckId"`
	Index  int    `json:"index"`
}

// resolumeClip is one element of clips or persistentClips. DeckID is a
// pointer, not a plain string: a persistent clip carries no "deckId" key
// on the wire at all (ADR-032 decision 6 — "PersistentClips ... live
// outside any deck and resolve regardless of selection"), and this
// program must be able to tell "no deck" apart from "deck with an empty
// id" rather than collapsing both to "".
//
// TransportTypeIndex, Width and Height are pointers, matching the
// server's own [omitempty] *int fields (internal/coordinator/api/v1/types.go's
// ResolumeCompositionClip) — review finding B: an earlier version of this
// type declared these as plain ints, so a clip with NO TransportType
// param (the key entirely absent from the response) decoded to the exact
// same 0 this program would print for a clip whose TransportType really
// is index 0, a measured zero indistinguishable from an absent one. This
// is the same class of defect CLAUDE.md records for FPP's own `ma`
// telemetry ("a JSON null is not an absent key") applied to a plain
// missing key rather than an explicit null.
//
// TransportTypeIndex is printed exactly as received, never translated.
// ADR-032's own bench capture establishes only that the option labels are
// not present in the composition file and vary per clip over the live
// API — this program does not know what any particular index means and
// must never imply that it does by inventing a label for it.
type resolumeClip struct {
	ID                 string  `json:"id"`
	DeckID             *string `json:"deckId"`
	LayerIndex         int     `json:"layerIndex"`
	ColumnIndex        int     `json:"columnIndex"`
	Name               string  `json:"name"`
	TransportTypeIndex *int    `json:"transportTypeIndex,omitempty"`
	SourcePath         string  `json:"sourcePath"`
	Width              *int    `json:"width,omitempty"`
	Height             *int    `json:"height,omitempty"`
}

// resolumeCompositionResponse is the body of a successful
// GET /api/v1/config/resolume/composition: the stored composition's own
// summary plus the full id map. ServerTime is optional for the identical
// reason resolumeCompositionUploadResponse.ServerTime is — see that
// field's own doc comment.
type resolumeCompositionResponse struct {
	ServerTime      *time.Time                 `json:"serverTime"`
	Revision        int64                      `json:"revision"`
	ActivatedAt     time.Time                  `json:"activatedAt"`
	Composition     resolumeCompositionSummary `json:"composition"`
	Decks           []resolumeDeckSummary      `json:"decks"`
	LayerGroups     []resolumeLayerGroup       `json:"layerGroups"`
	Layers          []resolumeLayer            `json:"layers"`
	Columns         []resolumeColumn           `json:"columns"`
	Clips           []resolumeClip             `json:"clips"`
	PersistentClips []resolumeClip             `json:"persistentClips"`
}

// cmdResolume implements "showmeshctl resolume".
func cmdResolume(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printResolumeUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printResolumeUsage(stdout)
		return exitOK
	case "composition":
		return cmdResolumeComposition(rest, stdout, stderr, clock)
	case "action":
		return cmdResolumeAction(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl resolume: unknown subcommand %q\n\n", sub)
		printResolumeUsage(stderr)
		return exitUsage
	}
}

func printResolumeUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl resolume <subcommand> [flags]

Subcommands:
  composition   upload or show the stored Resolume Arena composition: the
                id map of decks, layers, columns and clips every ShowMesh
                reference to a Resolume object resolves through
  action        dispatch one of the seven Resolume actions (launch a clip,
                clear a layer, blackout, ...), or list the vocabulary

Run "showmeshctl resolume composition --help" or
"showmeshctl resolume action --help" for their own subcommands.
`)
}

// cmdResolumeComposition implements "showmeshctl resolume composition".
func cmdResolumeComposition(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printResolumeCompositionUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printResolumeCompositionUsage(stdout)
		return exitOK
	case "upload":
		return cmdResolumeCompositionUpload(rest, stdout, stderr, clock)
	case "show":
		return cmdResolumeCompositionShow(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl resolume composition: unknown subcommand %q\n\n", sub)
		printResolumeCompositionUsage(stderr)
		return exitUsage
	}
}

func printResolumeCompositionUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl resolume composition <subcommand> [flags]

Read or write the Resolume Arena composition ShowMesh has stored: the id
map of decks, layers, columns and clips every ShowMesh reference to a
Resolume object resolves through. The composition is never read live from
Resolume at request time; it comes from a file the operator uploads. Every
subcommand requires the config:write scope (admin only) — reading this
surface is exactly as sensitive as writing it.

Subcommands:
  upload <path>   parse and store a composition file (.avc), replacing
                  whatever composition was stored before. Requires
                  config:write. Prints what was found — composition name,
                  the Arena version that wrote it, canvas size, deck names
                  with clip counts, and layer/clip/persistent counts — not
                  a success tick, so you can confirm this is the show you
                  meant to upload.
  show            show the stored composition. Every clip is grouped
                  under its own deck (or, for a persistent clip, under its
                  own section) because a clip id means nothing without
                  knowing which deck it belongs to. Add --json for the raw
                  document. Requires config:write.

Run "showmeshctl resolume composition <subcommand> --help" for flags
specific to one subcommand.
`)
}

// minResolumeUploadClientTimeout is this command's own floor on
// --timeout, mirroring cmd_fpp_command.go's identical pattern: a request
// budget smaller than what the coordinator needs to receive, parse, and
// store a composition file can only ever abort a healthy, still-working
// upload and report it as a transport failure. SHOWMESH HYPOTHESIS, NOT
// MEASURED — unlike minFPPCommandClientTimeout, no documented server-side
// deadline exists to reconcile this against; 30s was chosen only as
// "clearly larger than the 10s global default, for a request that also
// has to transfer a file that can run several hundred kilobytes."
const minResolumeUploadClientTimeout = 30 * time.Second

// effectiveResolumeUploadTimeout returns the request budget
// "resolume composition upload" actually uses: flagTimeout when it is
// already at least [minResolumeUploadClientTimeout], and the floor
// otherwise. Matches effectiveFPPCommandTimeout's own "raise, never
// refuse" posture in cmd_fpp_command.go.
func effectiveResolumeUploadTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minResolumeUploadClientTimeout {
		return minResolumeUploadClientTimeout
	}
	return flagTimeout
}

// cmdResolumeCompositionUpload implements
// "showmeshctl resolume composition upload <path>"
// (POST /api/v1/config/resolume/composition, multipart/form-data, one
// file part named "file"). The file is opened, stat'd, and confirmed to
// be a regular file entirely before any client or request is built: a
// missing or unreadable path must never reach the network as a confusing
// transport failure when it is actually a usage error this program could
// have caught for free.
func cmdResolumeCompositionUpload(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume composition upload", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume composition upload <path/to/file.avc> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nUpload a Resolume Arena composition file and print what the coordinator")
		_, _ = fmt.Fprintln(stderr, "parsed from it: composition name, the Arena version that wrote it, canvas")
		_, _ = fmt.Fprintln(stderr, "size, deck names with clip counts, and layer/clip/persistent-clip counts.")
		_, _ = fmt.Fprintln(stderr, "Requires config:write. This replaces whatever composition was stored")
		_, _ = fmt.Fprintln(stderr, "before; a rejected file changes nothing.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume composition upload", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	path := rest[0]

	f, err := os.Open(path)
	if err != nil {
		return reportError(stderr, "resolume composition upload", newCLIError(exitUsage, "cannot read %s: %v", path, err))
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return reportError(stderr, "resolume composition upload", newCLIError(exitUsage, "cannot read %s: %v", path, err))
	}
	if info.IsDir() {
		return reportError(stderr, "resolume composition upload", newCLIError(exitUsage, "%s is a directory, not a composition file", path))
	}

	body, contentType, err := buildCompositionMultipartBody("file", filepath.Base(path), f)
	if err != nil {
		return reportError(stderr, "resolume composition upload", newCLIError(exitUsage, "reading %s: %v", path, err))
	}

	timeout := effectiveResolumeUploadTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl resolume composition upload: --timeout %s is below this command's own minimum request "+
				"budget of %s; using %s instead.\n", g.timeout, minResolumeUploadClientTimeout, timeout)
	}

	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, "resolume composition upload", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	const apiPath = "/api/v1/config/resolume/composition"
	raw, err := postComposition(ctx, c, apiPath, contentType, body)
	if err != nil {
		return reportError(stderr, "resolume composition upload", err)
	}

	// Decoded separately from what --output json prints (below): this
	// program's own resolumeCompositionUploadResponse is read here only for
	// the clock-skew check and the default text rendering, never for the
	// JSON output itself. Decoding into a Go struct and re-marshaling it
	// would re-serialize ADR-032 decision 6's own omitted-when-persistent
	// "deckId" as an explicit JSON null (a *string zero value marshals that
	// way), inventing a key the coordinator never sent — see printJSON's own
	// documented general behavior in printers.go, which this command's
	// --output json deliberately does NOT follow for that reason.
	var resp resolumeCompositionUploadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return reportError(stderr, "resolume composition upload", newCLIError(exitAPIError, "decoding response from %s: %v", c.endpoint(apiPath, nil), err))
	}

	if resp.ServerTime != nil {
		printClockSkew(stderr, *resp.ServerTime, clock())
	}

	if g.output == outputJSON {
		if err := printResolumeCompositionRawJSON(stdout, raw); err != nil {
			return reportError(stderr, "resolume composition upload", err)
		}
		return exitOK
	}
	printResolumeCompositionUploadResult(stdout, resp)
	return exitOK
}

// buildCompositionMultipartBody builds a real multipart/form-data body
// carrying r's bytes as a single file part named fieldName. The whole
// body is buffered in memory rather than streamed: a composition file is
// hundreds of kilobytes (ADR-032: 407 KB for the reference show), well
// under the response-side maxResponseBytes bound this program already
// accepts elsewhere, so buffering it is simple and correct rather than a
// real memory concern.
func buildCompositionMultipartBody(fieldName, filename string, r io.Reader) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(fieldName, filename)
	if err != nil {
		return nil, "", fmt.Errorf("building multipart body: %w", err)
	}
	if _, err := io.Copy(part, r); err != nil {
		return nil, "", fmt.Errorf("reading file contents: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("finishing multipart body: %w", err)
	}
	return &buf, mw.FormDataContentType(), nil
}

// postComposition issues the authenticated multipart/form-data upload
// POST /api/v1/config/resolume/composition takes, and returns the raw
// success response body unmodified — never decoded here, so a caller that
// wants to print it verbatim (per this file's own --output json rule; see
// cmdResolumeCompositionUpload) has the coordinator's actual bytes to
// print, not a value already round-tripped through this program's own
// struct and back out. This is a request core distinct from client.go's
// writeJSON/postJSON/putJSON/deleteJSON: every other write this program
// issues carries an application/json body, and this is the first one that
// does not, so it does not fit [client.doWithBody]'s json.Marshal-only
// shape. It otherwise matches doWithBody's own bounded-read, status-check,
// and version-check behaviour exactly, and reuses client.go's own helpers
// (applyHeaders, checkAPIVersionHeader, decodeProblemError,
// classifyRequestError, maxResponseBytes) rather than duplicating their
// logic.
func postComposition(ctx context.Context, c *client, apiPath, contentType string, body *bytes.Buffer) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(apiPath, nil), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, newCLIError(exitUsage, "building request: %v", err)
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, classifyRequestError(c.baseURL.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, newCLIError(exitUnreachable, "reading response body: %v", err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		return nil, newCLIError(exitAPIError, "%v (from %s)", errResponseTooLarge, c.endpoint(apiPath, nil))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeProblemError(resp, respBody)
	}
	if err := c.checkAPIVersionHeader(resp); err != nil {
		return nil, err
	}
	return respBody, nil
}

// printResolumeCompositionRawJSON writes raw — the coordinator's own
// response bytes for a resolume composition read or write — to w
// unmodified: no decode into this program's own structs, no re-marshal,
// no reformatting. This is the fix for a defect measured live: the
// coordinator omits "deckId" entirely from a persistent clip (ADR-032
// decision 6), but decoding into resolumeClip (DeckID *string) and
// handing the result to this program's general-purpose printJSON
// (printers.go) re-marshaled that absence as an explicit
// `"deckId": null` — a key the server never sent, invented by this
// program's own zero-value encoding. This command's own help text calls
// `show --json`'s output "the raw document"; printJSON's documented
// behavior (main.go's --output json flag help: "re-serializes this CLI's
// OWN decoded structs, not the coordinator's raw response bytes") is
// right for every other command in this program, which only promises
// forward-compatible decoding, never a byte-for-byte echo — but it is
// wrong for this one, because a client may ignore a field it does not
// recognize, it may not invent one the server did not send. A newline is
// appended only if raw does not already end in one, matching printJSON's
// own json.Encoder.Encode behavior (which always terminates its output
// with "\n") closely enough that both output modes are equally
// pipeline-friendly.
func printResolumeCompositionRawJSON(w io.Writer, raw []byte) error {
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
	}
	return nil
}

// printResolumeCompositionUploadResult renders a successful upload
// (ADR-032 decision 7): what was parsed, not a success tick.
func printResolumeCompositionUploadResult(w io.Writer, resp resolumeCompositionUploadResponse) {
	_, _ = fmt.Fprintf(w, "revision:     %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "activated at: %s\n\n", resp.ActivatedAt.Format(time.RFC3339))
	printResolumeCompositionSummary(w, resp.Composition)
}

// printResolumeCompositionSummary renders the composition summary object
// shared by both the upload response and the stored-composition read.
func printResolumeCompositionSummary(w io.Writer, c resolumeCompositionSummary) {
	_, _ = fmt.Fprintf(w, "name:         %s\n", c.Name)
	_, _ = fmt.Fprintf(w, "source file:  %s (%s)\n", c.SourceFilename, formatByteSize(c.SizeBytes))
	_, _ = fmt.Fprintf(w, "written by:   %s\n", c.WrittenBy.String())
	_, _ = fmt.Fprintf(w, "canvas:       %dx%d\n", c.Canvas.Width, c.Canvas.Height)
	_, _ = fmt.Fprintf(w, "layers:       %d (%d layer groups)\n", c.LayerCount, c.LayerGroupCount)
	_, _ = fmt.Fprintf(w, "columns:      %d\n", c.ColumnCount)
	_, _ = fmt.Fprintf(w, "clips:        %d (%d persistent)\n", c.ClipCount, c.PersistentClipCount)
	_, _ = fmt.Fprintln(w, "decks:")
	if len(c.Decks) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  ID\tNAME\tCLOSED\tCLIPS")
	for _, d := range c.Decks {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%t\t%d\n", d.ID, d.Name, d.Closed, d.ClipCount)
	}
	_ = tw.Flush()
}

// formatByteSize renders n bytes as a short, human-scaled string ("4.6
// KiB", "407 B"), for the source file size line — plain bytes for
// anything under 1 KiB, KiB above it: real composition files measured
// during ADR-032's bench capture ran from tens of KB to a few MB, so this
// never needs to reach MiB to stay readable.
func formatByteSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

// resolumeCompositionNotUploadedMessage is printed by `show` when the
// coordinator reports no composition stored yet — an ordinary, expected
// state (a fresh coordinator, or one whose operator has not uploaded a
// show yet). The message stays a plain one-liner naming the remedy rather
// than an alarming error, but the command still exits exitNotFound (see
// cmdResolumeCompositionShow below) — matching cmdConfigGet's identical
// unset-configuration case, so a caller branching on $? sees the same
// signal from both configuration surfaces.
const resolumeCompositionNotUploadedMessage = "no Resolume composition has been uploaded yet; run \"showmeshctl resolume composition upload <path/to/file.avc>\" to add one"

// cmdResolumeCompositionShow implements
// "showmeshctl resolume composition show"
// (GET /api/v1/config/resolume/composition). Requires config:write,
// exactly like every other configuration surface this program talks to
// (config get/set): the wire contract gates this read behind config:write
// the same way it gates GET /config/fpp.endpoints, so this command is no
// more likely to succeed unauthenticated than cmdConfigGet is.
func cmdResolumeCompositionShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume composition show", stderr)
	var jsonOut bool
	fs.BoolVar(&jsonOut, "json", false, "print the raw stored composition document (same as --output json)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume composition show [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the stored Resolume composition: its summary plus the full id map")
		_, _ = fmt.Fprintln(stderr, "(decks, layers, columns, clips, persistent clips). Every clip is grouped")
		_, _ = fmt.Fprintln(stderr, "under its own deck, or under its own section if it is persistent, because")
		_, _ = fmt.Fprintln(stderr, "a clip id means nothing without knowing which deck it belongs to.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume composition show", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume composition show", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const apiPath = "/api/v1/config/resolume/composition"
	raw, err := c.getRaw(ctx, apiPath, nil)
	if err != nil {
		// A composition that has never been uploaded is a normal,
		// expected state — the plain one-line message names the remedy
		// rather than reading as a scary failure — but this command still
		// exits exitNotFound, matching cmdConfigGet's identical
		// unset-configuration case exactly (config.go's server-side doc
		// comment, and this file's own doc comment above
		// resolumeCompositionNotUploadedMessage): two configuration
		// commands answering the same server condition with different
		// exit codes is a defect in exactly the tool an operator reaches
		// for when the show is already broken and the UI is down, and a
		// distinct exit code is what makes the state scriptable. Any
		// OTHER error (unreachable coordinator, malformed response, an
		// unrelated 4xx/5xx) still reports exactly as every other read in
		// this program does.
		var ce *cliError
		if errors.As(err, &ce) && ce.code == exitNotFound {
			_, _ = fmt.Fprintln(stdout, resolumeCompositionNotUploadedMessage)
			return exitNotFound
		}
		return reportError(stderr, "resolume composition show", err)
	}

	// Decoded separately from what --json/--output json prints (below): see
	// printResolumeCompositionRawJSON's own doc comment for why this
	// command prints raw, the coordinator's own bytes, rather than
	// re-marshaling this program's decoded resolumeCompositionResponse the
	// way printJSON does for every other command.
	var resp resolumeCompositionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return reportError(stderr, "resolume composition show", newCLIError(exitAPIError, "decoding response from %s: %v", c.endpoint(apiPath, nil), err))
	}

	if resp.ServerTime != nil {
		printClockSkew(stderr, *resp.ServerTime, clock())
	}

	if jsonOut || g.output == outputJSON {
		if err := printResolumeCompositionRawJSON(stdout, raw); err != nil {
			return reportError(stderr, "resolume composition show", err)
		}
		return exitOK
	}
	printResolumeCompositionDetail(stdout, resp)
	return exitOK
}

// printResolumeCompositionDetail renders the stored composition for
// `show`'s default text output. Clip data is the one part of this
// document ADR-032 decision 6 says can actively mislead if printed
// carelessly (a bare clip id resolves over the live API only while its
// own deck is selected), so clips are always printed grouped under an
// explicit deck header, or under their own "persistent" section — never
// as a flat list of ids.
func printResolumeCompositionDetail(w io.Writer, resp resolumeCompositionResponse) {
	_, _ = fmt.Fprintf(w, "revision:     %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "activated at: %s\n\n", resp.ActivatedAt.Format(time.RFC3339))
	printResolumeCompositionSummary(w, resp.Composition)

	printResolumeCompositionIDMap(w, resp)

	deckName := make(map[string]string, len(resp.Decks))
	var deckOrder []string
	for _, d := range resp.Decks {
		deckName[d.ID] = d.Name
		deckOrder = append(deckOrder, d.ID)
	}
	sort.Strings(deckOrder)

	byDeck := make(map[string][]resolumeClip, len(deckOrder))
	for _, clip := range resp.Clips {
		id := ""
		if clip.DeckID != nil {
			id = *clip.DeckID
		}
		if _, known := deckName[id]; !known {
			if _, seen := byDeck[id]; !seen {
				deckOrder = append(deckOrder, id)
				sort.Strings(deckOrder)
			}
		}
		byDeck[id] = append(byDeck[id], clip)
	}

	_, _ = fmt.Fprintln(w, "\nclips by deck (a clip id means nothing without its deck):")
	for _, id := range deckOrder {
		clips := byDeck[id]
		label := id
		if name := deckName[id]; name != "" {
			label = fmt.Sprintf("%s (%s)", name, id)
		}
		_, _ = fmt.Fprintf(w, "  deck %s — %d clip(s):\n", label, len(clips))
		printResolumeClipsTable(w, clips)
	}
	if len(deckOrder) == 0 {
		_, _ = fmt.Fprintln(w, "  (no decks)")
	}

	_, _ = fmt.Fprintf(w, "\npersistent clips (%d, no deck — live outside any deck):\n", len(resp.PersistentClips))
	printResolumeClipsTable(w, resp.PersistentClips)
}

// printResolumeCompositionIDMap renders the id map's structural relations
// — which group a layer belongs to, which deck a column belongs to — as
// real tables, not just counts.
//
// Review finding A: an earlier version of this function printed only "N
// layer groups, N layers, N columns (see --json for these by id)", so the
// layer-to-group and column-to-deck relations — the structural half of
// the id map ADR-032 decision 1 exists to store — never appeared in the
// default text output at all, only in --json. The CLI is the "the show is
// broken and the UI is down" path (ADR-030); it must not make the id map
// readable only in JSON.
func printResolumeCompositionIDMap(w io.Writer, resp resolumeCompositionResponse) {
	_, _ = fmt.Fprintf(w, "\nlayer groups (%d):\n", len(resp.LayerGroups))
	if len(resp.LayerGroups) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	} else {
		tw := newTabWriter(w)
		_, _ = fmt.Fprintln(tw, "  ID\tINDEX")
		for _, lg := range resp.LayerGroups {
			_, _ = fmt.Fprintf(tw, "  %s\t%d\n", lg.ID, lg.Index)
		}
		_ = tw.Flush()
	}

	_, _ = fmt.Fprintf(w, "\nlayers (%d):\n", len(resp.Layers))
	if len(resp.Layers) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	} else {
		tw := newTabWriter(w)
		_, _ = fmt.Fprintln(tw, "  ID\tNAME\tINDEX\tLAYER GROUP INDEX")
		for _, l := range resp.Layers {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\n", l.ID, formatResolumeLayerName(l), l.Index, formatIntPtr(l.LayerGroupIndex))
		}
		_ = tw.Flush()
	}

	_, _ = fmt.Fprintf(w, "\ncolumns (%d):\n", len(resp.Columns))
	if len(resp.Columns) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	} else {
		tw := newTabWriter(w)
		_, _ = fmt.Fprintln(tw, "  ID\tDECK\tINDEX")
		for _, c := range resp.Columns {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d\n", c.ID, c.DeckID, c.Index)
		}
		_ = tw.Flush()
	}
}

// formatResolumeLayerName renders a layer's name for the text table,
// marking a coordinator-generated label as generated (ADR-037 decision 4)
// rather than letting it look like something the operator typed — the
// server's own l.Name is never blank, so this never has to fall back to a
// placeholder of its own.
func formatResolumeLayerName(l resolumeLayer) string {
	if l.NameGenerated {
		return l.Name + " (generated)"
	}
	return l.Name
}

// formatIntPtr renders an optional integer field for a text table: the
// value if present, or an explicit "(not present)" if the pointer is nil.
// This is review finding B's rendering half — see [resolumeClip]'s own
// doc comment for why TransportTypeIndex, Width and Height are pointers
// in the first place: a measured zero and an absent key must never render
// identically, the same rule CLAUDE.md records for FPP's own `ma`
// telemetry.
func formatIntPtr(p *int) string {
	if p == nil {
		return "(not present)"
	}
	return strconv.Itoa(*p)
}

// printResolumeClipsTable renders one deck's (or the persistent) clip
// list as a compact table. transportTypeIndex is printed as a raw index
// with an explicit label, never translated: see resolumeClip's own doc
// comment for why this program does not — and must not — invent a name
// for what any particular value means. transportTypeIndex, width and
// height each render "(not present)" rather than a plausible-looking 0
// when the server omitted the key — see [formatIntPtr].
func printResolumeClipsTable(w io.Writer, clips []resolumeClip) {
	if len(clips) == 0 {
		_, _ = fmt.Fprintln(w, "    (none)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "    ID\tNAME\tLAYER\tCOLUMN\tTRANSPORT INDEX (unlabeled)\tWIDTH\tHEIGHT\tSOURCE PATH")
	for _, c := range clips {
		_, _ = fmt.Fprintf(tw, "    %s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
			c.ID, c.Name, c.LayerIndex, c.ColumnIndex,
			formatIntPtr(c.TransportTypeIndex), formatIntPtr(c.Width), formatIntPtr(c.Height), c.SourcePath)
	}
	_ = tw.Flush()
}
