package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file is Track E seam E3/E4's own showmeshctl surface: list, get,
// upload, and fetch over POST/GET /api/v1/assets. Declares its own wire
// types rather than importing internal/coordinator/api/v1 (the
// import-graph test forbids it), matching cmd_show.go's precedent one
// kind over.
//
// "upload" and "fetch" are this program's second and third non-JSON
// request bodies (cmd_resolume_composition.go's "resolume composition
// upload" was the first) and, unlike that one, neither buffers its whole
// payload in memory first: an asset can run to gigabytes (ADR-028's
// operator numbers), so both stream — upload via a real io.Pipe multipart
// body written concurrently with the request, fetch by writing the
// response body straight to a temp file while hashing it. Progress is
// written to stderr as bytes move, and failure is stated rather than
// inferred (ADR-030 decision 4): a non-2xx prints the coordinator's own
// problem Detail, exactly like every other write this program issues.

// assetRecord mirrors v1.Asset.
type assetRecord struct {
	ID                     string     `json:"id"`
	Show                   string     `json:"show"`
	Sequence               string     `json:"sequence"`
	TargetKind             string     `json:"targetKind"`
	Target                 string     `json:"target"`
	MediaType              string     `json:"mediaType"`
	ContentHash            string     `json:"contentHash"`
	RuntimeFilename        string     `json:"runtimeFilename"`
	SizeBytes              int64      `json:"sizeBytes"`
	CreatedAt              time.Time  `json:"createdAt"`
	CreatedByPrincipalID   *string    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string    `json:"createdByPrincipalName"`
	SupersededAt           *time.Time `json:"supersededAt"`
	Current                bool       `json:"current"`
}

// assetResponse is the body of POST /api/v1/assets and GET /api/v1/assets/{id}.
type assetResponse struct {
	ServerTime time.Time   `json:"serverTime"`
	Asset      assetRecord `json:"asset"`
}

// assetsListResponse is the body of GET /api/v1/assets.
type assetsListResponse struct {
	ServerTime time.Time     `json:"serverTime"`
	Assets     []assetRecord `json:"assets"`
}

// --- manifest wire types, mirroring v1.NodeAssetManifest and friends ---

type missingAssetRecord struct {
	AssetID     string `json:"assetId"`
	Sequence    string `json:"sequence"`
	Filename    string `json:"filename"`
	ContentHash string `json:"contentHash"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type assetGapRecord struct {
	Sequence string   `json:"sequence"`
	Surfaces []string `json:"surfaces"`
}

type extraAssetRecord struct {
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// nodeAssetManifestRecord mirrors v1.NodeAssetManifest. State is one of
// "ready", "not_ready", "unknown"; Reason is non-nil whenever State is not
// "ready" (ADR-020).
type nodeAssetManifestRecord struct {
	Node       string               `json:"node"`
	State      string               `json:"state"`
	Reason     *string              `json:"reason"`
	Missing    []missingAssetRecord `json:"missing"`
	Gaps       []assetGapRecord     `json:"gaps"`
	Extra      []extraAssetRecord   `json:"extra"`
	ObservedAt *time.Time           `json:"observedAt"`
}

// nodeAssetManifestResponse is the body of GET /api/v1/nodes/{nodeId}/assets.
type nodeAssetManifestResponse struct {
	ServerTime time.Time               `json:"serverTime"`
	Manifest   nodeAssetManifestRecord `json:"manifest"`
}

// assetManifestResponse is the body of GET /api/v1/assets/manifest.
type assetManifestResponse struct {
	ServerTime time.Time                 `json:"serverTime"`
	Nodes      []nodeAssetManifestRecord `json:"nodes"`
}

// --- the timeout budget: restated from assetstore.UploadBudget ---
//
// showmeshctl may not import a coordinator package (the import-graph test
// enforces it — importgraph_test.go), so these three values are this
// program's own copy of internal/coordinator/assetstore's
// DefaultMaxUploadBytes/MinTransferBytesPerSecond/UploadBudget. A comment
// saying "keep these in sync" is not a mechanism: cmd_assets_test.go's
// TestAssetUploadBudgetMatchesServer imports that package (test files are
// exempt from the import-graph rule — it checks only `go list -deps .`,
// the production build) and asserts these values and this function agree
// with it, so a server-side change that silently drifts this copy is
// caught by a real assertion rather than by someone re-reading a comment.

// assetDefaultMaxUploadBytes must match assetstore.DefaultMaxUploadBytes.
const assetDefaultMaxUploadBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GiB

// assetMinTransferBytesPerSecond must match assetstore.MinTransferBytesPerSecond.
const assetMinTransferBytesPerSecond int64 = 1024 * 1024 // 1 MiB/s

const (
	assetUploadBudgetGrace   = 30 * time.Second
	assetUploadBudgetCeiling = 2 * time.Hour
)

// assetUploadBudget must compute IDENTICALLY to assetstore.UploadBudget —
// see this section's own doc comment for how that identity is enforced.
func assetUploadBudget(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	transferSeconds := float64(size) / float64(assetMinTransferBytesPerSecond)
	budget := time.Duration(transferSeconds*float64(time.Second)) + assetUploadBudgetGrace
	if budget > assetUploadBudgetCeiling {
		return assetUploadBudgetCeiling
	}
	return budget
}

// effectiveAssetTransferTimeout raises flagTimeout to at least
// assetUploadBudget(size) when it is smaller, matching
// effectiveResolumeUploadTimeout's "raise, never refuse" posture one file
// over: a transfer budget derived from the file's own size, not a single
// hand-picked constant every upload and download shares regardless of how
// large it is.
func effectiveAssetTransferTimeout(flagTimeout time.Duration, size int64) time.Duration {
	budget := assetUploadBudget(size)
	if flagTimeout > budget {
		return flagTimeout
	}
	return budget
}

// cmdAssets implements "showmeshctl assets".
func cmdAssets(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAssetsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAssetsUsage(stdout)
		return exitOK
	case "list":
		return cmdAssetsList(rest, stdout, stderr, clock)
	case "get":
		return cmdAssetsGet(rest, stdout, stderr, clock)
	case "upload":
		return cmdAssetsUpload(rest, stdout, stderr, clock)
	case "fetch":
		return cmdAssetsFetch(rest, stdout, stderr, clock)
	case "manifest":
		return cmdAssetsManifest(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl assets: unknown subcommand %q\n\n", sub)
		printAssetsUsage(stderr)
		return exitUsage
	}
}

func printAssetsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl assets <subcommand> [flags]

Read or write the coordinator's asset store (Track E, ADR-028): FSEQ,
audio, and other show media, identified by show + sequence + target +
content hash, never by filename alone. Reads require show:macro:run OR
config:write, matching "show"/"surface". Writing (upload) requires
asset:write (admin only).

Subcommands:
  list             enumerate asset metadata, optionally narrowed by
                   --show/--node/--sequence
  get <assetId>    show one asset's full metadata
  upload           stream a file into the store and register its metadata
                   (write, requires asset:write)
  fetch <assetId>  download one asset's bytes, verifying the content hash
                   before the file lands at --out
  manifest         show what each node should hold for the active show
                   versus what it actually holds (Track E seam E5)

Run "showmeshctl assets <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdAssetsList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets list", stderr)
	var show, node, sequence string
	fs.StringVar(&show, "show", "", "narrow the list to this show id")
	fs.StringVar(&node, "node", "", "narrow the list to CURRENT assets targeted at this node (never a show-wide asset)")
	fs.StringVar(&sequence, "sequence", "", "narrow the list to this logical sequence id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate asset metadata (GET /api/v1/assets).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "assets list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	query := url.Values{}
	if show != "" {
		query.Set("show", show)
	}
	if node != "" {
		query.Set("node", node)
	}
	if sequence != "" {
		query.Set("sequence", sequence)
	}

	var resp assetsListResponse
	if err := c.getJSON(ctx, "/api/v1/assets", query, &resp); err != nil {
		return reportError(stderr, "assets list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "assets list", err)
		}
		return exitOK
	}
	printAssetsTable(stdout, resp)
	return exitOK
}

func cmdAssetsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets get [flags] <assetId>")
		_, _ = fmt.Fprintln(stderr, "\nShow one asset's full metadata (GET /api/v1/assets/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "assets get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp assetResponse
	if err := c.getJSON(ctx, "/api/v1/assets/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "assets get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "assets get", err)
		}
		return exitOK
	}
	printAssetDetail(stdout, resp.Asset)
	return exitOK
}

// --- upload ---

// progressReader wraps an io.Reader, writing a throttled "N / total (P%)"
// line to w as bytes are read — this program's own version of ADR-030
// decision 5's "progress and failure are stated, never inferred", applied
// to a CLI rather than a browser progress bar. Throttled by wall-clock
// time, not by call count, so a fast local upload does not spam stderr
// and a slow one still shows visible movement.
type progressReader struct {
	r         io.Reader
	label     string
	w         io.Writer
	total     int64
	read      int64
	lastPrint time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	now := time.Now()
	done := errors.Is(err, io.EOF)
	if done || now.Sub(p.lastPrint) >= 500*time.Millisecond {
		p.lastPrint = now
		if p.total > 0 {
			pct := float64(p.read) / float64(p.total) * 100
			_, _ = fmt.Fprintf(p.w, "\r%s: %s / %s (%.0f%%)", p.label, formatByteSize(p.read), formatByteSize(p.total), pct)
		} else {
			_, _ = fmt.Fprintf(p.w, "\r%s: %s", p.label, formatByteSize(p.read))
		}
		if done {
			_, _ = fmt.Fprintln(p.w)
		}
	}
	return n, err
}

// buildAssetUploadBody starts a goroutine that streams fields (in the
// coordinator's own required order: every field before the file part —
// assets.go's readAssetUploadFilePart refuses a "file" part that arrives
// first) and then f's bytes, wrapped in a progressReader, into an
// io.Pipe. The returned reader is the request body; the goroutine's own
// error, if any, is delivered by closing the pipe with it
// (io.PipeWriter.CloseWithError), which surfaces as the read error the
// HTTP request itself then reports.
func buildAssetUploadBody(fields map[string]string, filename string, f io.Reader, size int64, stderr io.Writer) (io.ReadCloser, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		var werr error
		defer func() { _ = pw.CloseWithError(werr) }()

		for _, k := range []string{"show", "sequence", "mediaType", "targetKind", "target"} {
			v, ok := fields[k]
			if !ok {
				continue
			}
			if werr = mw.WriteField(k, v); werr != nil {
				return
			}
		}

		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			werr = err
			return
		}
		pw2 := &progressReader{r: f, label: "uploading " + filename, w: stderr, total: size}
		if _, err := io.Copy(fw, pw2); err != nil {
			werr = fmt.Errorf("reading %s: %w", filename, err)
			return
		}
		werr = mw.Close()
	}()

	return pr, contentType
}

func cmdAssetsUpload(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets upload", stderr)
	var show, sequence, mediaType, targetKind, target, file string
	fs.StringVar(&show, "show", "", "the show this asset belongs to (required)")
	fs.StringVar(&sequence, "sequence", "", "the logical sequence id (required)")
	fs.StringVar(&mediaType, "media-type", "", `one of "fseq", "audio", "media" (required)`)
	fs.StringVar(&targetKind, "target-kind", "", `"node" or "show" (required, no default)`)
	fs.StringVar(&target, "target", "", "the declared node id (required when --target-kind=node)")
	fs.StringVar(&file, "file", "", "path to the file to upload (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets upload [flags]")
		_, _ = fmt.Fprintln(stderr, "\nStream a file into the asset store and register its metadata")
		_, _ = fmt.Fprintln(stderr, "(POST /api/v1/assets, multipart/form-data). Requires asset:write.")
		_, _ = fmt.Fprintln(stderr, "\nRe-uploading IDENTICAL bytes for the same show/sequence/target is")
		_, _ = fmt.Fprintln(stderr, "idempotent (prints the existing asset, no new row). Different bytes for")
		_, _ = fmt.Fprintln(stderr, "the same identity supersede the previous current asset.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets upload", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	for name, v := range map[string]string{"show": show, "sequence": sequence, "media-type": mediaType, "target-kind": targetKind, "file": file} {
		if v == "" {
			_, _ = fmt.Fprintf(stderr, "showmeshctl assets upload: --%s is required\n", name)
			return exitUsage
		}
	}
	if targetKind == "node" && target == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl assets upload: --target is required when --target-kind=node")
		return exitUsage
	}

	f, err := os.Open(file)
	if err != nil {
		return reportError(stderr, "assets upload", newCLIError(exitUsage, "cannot read %s: %v", file, err))
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return reportError(stderr, "assets upload", newCLIError(exitUsage, "cannot read %s: %v", file, err))
	}
	if info.IsDir() {
		return reportError(stderr, "assets upload", newCLIError(exitUsage, "%s is a directory, not a file", file))
	}

	timeout := effectiveAssetTransferTimeout(g.timeout, info.Size())
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl assets upload: --timeout %s is below this upload's own derived budget of %s for a %s file; using %s instead.\n",
			g.timeout, timeout, formatByteSize(info.Size()), timeout)
	}

	fields := map[string]string{"show": show, "sequence": sequence, "mediaType": mediaType, "targetKind": targetKind, "target": target}
	body, contentType := buildAssetUploadBody(fields, filepath.Base(file), f, info.Size(), stderr)
	defer func() { _ = body.Close() }()

	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, "assets upload", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	raw, err := postAssetMultipart(ctx, c, "/api/v1/assets", contentType, body)
	if err != nil {
		return reportError(stderr, "assets upload", err)
	}

	var resp assetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return reportError(stderr, "assets upload", newCLIError(exitAPIError, "decoding response: %v", err))
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "assets upload", err)
		}
		return exitOK
	}
	printAssetDetail(stdout, resp.Asset)
	return exitOK
}

// postAssetMultipart issues the authenticated multipart/form-data POST
// upload takes and returns the raw success response body — mirrors
// postComposition's request core (cmd_resolume_composition.go) except
// body is a streaming io.Reader (an io.Pipe, ultimately), never a buffer,
// since an asset is not guaranteed to fit in memory the way a composition
// file is.
func postAssetMultipart(ctx context.Context, c *client, apiPath, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(apiPath, nil), body)
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

// --- fetch ---

func cmdAssetsFetch(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets fetch", stderr)
	var out string
	fs.StringVar(&out, "out", "", "path to write the downloaded file to (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets fetch [flags] <assetId>")
		_, _ = fmt.Fprintln(stderr, "\nDownload one asset's bytes (GET /api/v1/assets/{id}/content),")
		_, _ = fmt.Fprintln(stderr, "verifying the content hash BEFORE the file lands at --out: the bytes")
		_, _ = fmt.Fprintln(stderr, "are written to a temp path first and renamed into place only once the")
		_, _ = fmt.Fprintln(stderr, "hash matches. A mismatch removes the temp file and reports failure;")
		_, _ = fmt.Fprintln(stderr, "--out is never touched.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets fetch", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if out == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl assets fetch: --out is required")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "assets fetch", err)
	}
	metaCtx, metaCancel := context.WithTimeout(context.Background(), g.timeout)
	defer metaCancel()

	var meta assetResponse
	if err := c.getJSON(metaCtx, "/api/v1/assets/"+url.PathEscape(id), nil, &meta); err != nil {
		return reportError(stderr, "assets fetch", err)
	}

	timeout := effectiveAssetTransferTimeout(g.timeout, meta.Asset.SizeBytes)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl assets fetch: --timeout %s is below this download's own derived budget of %s for a %s asset; using %s instead.\n",
			g.timeout, timeout, formatByteSize(meta.Asset.SizeBytes), timeout)
	}

	dlClient, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, "assets fetch", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlClient.endpoint("/api/v1/assets/"+url.PathEscape(id)+"/content", nil), nil)
	if err != nil {
		return reportError(stderr, "assets fetch", newCLIError(exitUsage, "building request: %v", err))
	}
	dlClient.applyHeaders(req)

	resp, err := dlClient.httpClient.Do(req)
	if err != nil {
		return reportError(stderr, "assets fetch", classifyRequestError(dlClient.baseURL.String(), err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		problemBody, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if rerr != nil {
			return reportError(stderr, "assets fetch", newCLIError(exitUnreachable, "reading response body: %v", rerr))
		}
		return reportError(stderr, "assets fetch", decodeProblemError(resp, problemBody))
	}

	outDir := filepath.Dir(out)
	tmp, err := os.CreateTemp(outDir, ".showmeshctl-asset-fetch-*")
	if err != nil {
		return reportError(stderr, "assets fetch", newCLIError(exitAPIError, "creating temp file in %s: %v", outDir, err))
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	pr := &progressReader{r: resp.Body, label: "fetching " + meta.Asset.RuntimeFilename, w: stderr, total: meta.Asset.SizeBytes}
	if _, err := io.Copy(io.MultiWriter(tmp, h), pr); err != nil {
		_ = tmp.Close()
		return reportError(stderr, "assets fetch", newCLIError(exitUnreachable, "downloading: %v", err))
	}
	if err := tmp.Close(); err != nil {
		return reportError(stderr, "assets fetch", newCLIError(exitAPIError, "closing temp file: %v", err))
	}

	gotHash := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if gotHash != meta.Asset.ContentHash {
		return reportError(stderr, "assets fetch", newCLIError(exitAPIError,
			"downloaded bytes do not match the recorded content hash (got %s, want %s); nothing was written to %s",
			gotHash, meta.Asset.ContentHash, out))
	}

	if err := os.Rename(tmpPath, out); err != nil {
		return reportError(stderr, "assets fetch", newCLIError(exitAPIError, "renaming into place at %s: %v", out, err))
	}
	cleanupTmp = false

	_, _ = fmt.Fprintf(stdout, "wrote %s (%s, %s) to %s\n", meta.Asset.RuntimeFilename, formatByteSize(meta.Asset.SizeBytes), meta.Asset.ContentHash, out)
	return exitOK
}

// --- manifest ---

// cmdAssetsManifest implements "showmeshctl assets manifest": "what should
// a node hold for the active show" versus "what does it actually hold"
// (Track E seam E5). Reporting never fails on its own — without
// --require-ready this always exits 0, no matter what state any node is
// in, because printing a table is not the same claim as gating a show.
func cmdAssetsManifest(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl assets manifest", stderr)
	var node string
	var requireReady bool
	fs.StringVar(&node, "node", "", "show only this node's manifest (default: every declared node)")
	fs.BoolVar(&requireReady, "require-ready", false,
		"exit 20 if any node is not_ready, or 21 if any node is unknown and none is not_ready; without this flag, always exit 0")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl assets manifest [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow what each node should hold for the active show, versus what it")
		_, _ = fmt.Fprintln(stderr, "actually holds (GET /api/v1/assets/manifest, or, with --node, GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/nodes/{nodeId}/assets for one node only).")
		_, _ = fmt.Fprintln(stderr, "\nReporting is not failing: without --require-ready this always exits 0,")
		_, _ = fmt.Fprintln(stderr, "regardless of any node's state. With --require-ready:")
		_, _ = fmt.Fprintln(stderr, "  exit 20  at least one node is not_ready (checked, and something is missing)")
		_, _ = fmt.Fprintln(stderr, "  exit 21  at least one node is unknown, and none is not_ready (cannot tell)")
		_, _ = fmt.Fprintln(stderr, "  exit 0   every node is ready")
		_, _ = fmt.Fprintln(stderr, "These are deliberately distinct: \"I checked and it is missing\" and \"I")
		_, _ = fmt.Fprintln(stderr, "cannot tell\" are different operational situations, and a script that")
		_, _ = fmt.Fprintln(stderr, "treats them the same will either start a show it should not, or block")
		_, _ = fmt.Fprintln(stderr, "one it should not.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "assets manifest", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "assets manifest", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var nodes []nodeAssetManifestRecord
	var serverTime time.Time
	if node != "" {
		var resp nodeAssetManifestResponse
		if err := c.getJSON(ctx, "/api/v1/nodes/"+url.PathEscape(node)+"/assets", nil, &resp); err != nil {
			return reportError(stderr, "assets manifest", err)
		}
		nodes = []nodeAssetManifestRecord{resp.Manifest}
		serverTime = resp.ServerTime
	} else {
		var resp assetManifestResponse
		if err := c.getJSON(ctx, "/api/v1/assets/manifest", nil, &resp); err != nil {
			return reportError(stderr, "assets manifest", err)
		}
		nodes = resp.Nodes
		serverTime = resp.ServerTime
	}
	printClockSkew(stderr, serverTime, clock())

	if g.output == outputJSON {
		out := struct {
			ServerTime time.Time                 `json:"serverTime"`
			Nodes      []nodeAssetManifestRecord `json:"nodes"`
		}{ServerTime: serverTime, Nodes: nodes}
		if err := printJSON(stdout, out); err != nil {
			return reportError(stderr, "assets manifest", err)
		}
	} else {
		printAssetManifestTable(stdout, nodes)
	}

	if !requireReady {
		return exitOK
	}
	anyNotReady, anyUnknown := false, false
	for _, n := range nodes {
		switch n.State {
		case "not_ready":
			anyNotReady = true
		case "unknown":
			anyUnknown = true
		}
	}
	switch {
	case anyNotReady:
		_, _ = fmt.Fprintln(stderr, "showmeshctl assets manifest: at least one node is not_ready")
		return exitAssetsNotReady
	case anyUnknown:
		_, _ = fmt.Fprintln(stderr, "showmeshctl assets manifest: at least one node is unknown (no node is not_ready)")
		return exitAssetsUnknown
	default:
		return exitOK
	}
}

// --- rendering ---

func printAssetsTable(w io.Writer, resp assetsListResponse) {
	if len(resp.Assets) == 0 {
		_, _ = fmt.Fprintln(w, "(no assets)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tSHOW\tSEQUENCE\tTARGET\tMEDIA TYPE\tSIZE\tCURRENT\tFILENAME")
	for _, a := range resp.Assets {
		target := a.Target
		if a.TargetKind == "show" {
			target = "(show)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n",
			a.ID, a.Show, a.Sequence, target, a.MediaType, formatByteSize(a.SizeBytes), a.Current, a.RuntimeFilename)
	}
	_ = tw.Flush()
}

func printAssetDetail(w io.Writer, a assetRecord) {
	_, _ = fmt.Fprintf(w, "id:               %s\n", a.ID)
	_, _ = fmt.Fprintf(w, "show:             %s\n", a.Show)
	_, _ = fmt.Fprintf(w, "sequence:         %s\n", a.Sequence)
	target := a.Target
	if a.TargetKind == "show" {
		target = "(whole show)"
	}
	_, _ = fmt.Fprintf(w, "target:           %s (%s)\n", target, a.TargetKind)
	_, _ = fmt.Fprintf(w, "media type:       %s\n", a.MediaType)
	_, _ = fmt.Fprintf(w, "content hash:     %s\n", a.ContentHash)
	_, _ = fmt.Fprintf(w, "runtime filename: %s\n", a.RuntimeFilename)
	_, _ = fmt.Fprintf(w, "size:             %s\n", formatByteSize(a.SizeBytes))
	_, _ = fmt.Fprintf(w, "created at:       %s\n", a.CreatedAt.Format(time.RFC3339))
	if a.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "created by:       %s\n", *a.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintln(w, "created by:       (no principal recorded)")
	}
	if a.Current {
		_, _ = fmt.Fprintln(w, "current:          yes")
	} else if a.SupersededAt != nil {
		_, _ = fmt.Fprintf(w, "current:          no (superseded at %s)\n", a.SupersededAt.Format(time.RFC3339))
	} else {
		_, _ = fmt.Fprintln(w, "current:          no")
	}
}

// printAssetManifestTable renders one summary row per node, then one
// detail line per missing asset and per gap — the summary alone tells an
// operator whether to worry; the detail lines say exactly what to fix.
func printAssetManifestTable(w io.Writer, nodes []nodeAssetManifestRecord) {
	if len(nodes) == 0 {
		_, _ = fmt.Fprintln(w, "(no nodes)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "NODE\tSTATE\tMISSING\tGAPS\tEXTRA\tREASON")
	for _, n := range nodes {
		reason := ""
		if n.Reason != nil {
			reason = *n.Reason
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", n.Node, n.State, len(n.Missing), len(n.Gaps), len(n.Extra), reason)
	}
	_ = tw.Flush()

	for _, n := range nodes {
		for _, m := range n.Missing {
			_, _ = fmt.Fprintf(w, "  %s: missing %q (sequence %s, %s)\n", n.Node, m.Filename, m.Sequence, m.ContentHash)
		}
		for _, g := range n.Gaps {
			_, _ = fmt.Fprintf(w, "  %s: no coverage for sequence %s (surfaces: %s)\n", n.Node, g.Sequence, strings.Join(g.Surfaces, ", "))
		}
	}
}
