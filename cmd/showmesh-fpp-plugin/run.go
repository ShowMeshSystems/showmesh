package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// defaultRunTimeout is this program's own request budget for one run
// submission. POST /api/v1/macros/{id}/runs answers 202 as soon as the run
// is accepted (section 6.6, ADR-031 decision 1) — it never waits for the
// run to finish — so this budget only needs to cover one ordinary HTTP
// round trip, unlike showmeshctl's "fpp <verb>" subcommands, which
// deliberately wait out a primitive's own confirmation deadline.
const defaultRunTimeout = 15 * time.Second

// cmdRun implements "showmesh-fpp-plugin run <macroId>": submit a macro
// run and record the outcome locally per section 8.3, classifying it into
// section 8.2's four classes (plus this program's own classLocalError for
// an attempt that never reached the coordinator at all).
func cmdRun(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs := flag.NewFlagSet("showmesh-fpp-plugin run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var configDirFlag string
	var timeout time.Duration
	fs.StringVar(&configDirFlag, "config-dir", "", "override this plugin's config directory")
	fs.DurationVar(&timeout, "timeout", defaultRunTimeout, "request timeout for the run submission")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmesh-fpp-plugin run <macroId> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nSubmit a macro run to this plugin's configured coordinator, and record")
		_, _ = fmt.Fprintln(stderr, "the outcome locally regardless of whether the coordinator could be reached.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	positional := fs.Args()
	if len(positional) != 1 || positional[0] == "" {
		fs.Usage()
		return exitUsage
	}
	macroID := positional[0]
	configDir := resolveConfigDir(configDirFlag)
	now := clock()

	token, err := loadCredential(configDir)
	if err != nil {
		return reportLocalError(stdout, stderr, configDir, macroID, now, err)
	}
	coordinatorURL, err := loadCoordinatorURL(configDir)
	if err != nil {
		return reportLocalError(stdout, stderr, configDir, macroID, now, err)
	}

	key, err := newIdempotencyKey()
	if err != nil {
		return reportLocalError(stdout, stderr, configDir, macroID, now, fmt.Errorf("generating an idempotency key: %w", err))
	}

	buffer, err := loadFailureBuffer(configDir)
	if err != nil {
		// A corrupt failure buffer must not block this run from being
		// attempted — proceed with an empty buffer rather than refusing
		// to run over a file this program can regenerate.
		buffer = failureBuffer{}
	}
	buffer.pruneByAge(now)
	priorFailures, priorFailuresDropped := buffer.asPriorFailures()

	body := createMacroRunRequest{
		IdempotencyKey:       key,
		Trigger:              "plugin",
		PriorFailures:        priorFailures,
		PriorFailuresDropped: priorFailuresDropped,
	}

	httpClient := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result := submitMacroRun(ctx, httpClient, coordinatorURL, token, macroID, body)

	switch result.Class {
	case classOK:
		return reportOK(ctx, httpClient, stdout, stderr, configDir, macroID, coordinatorURL, token, now, result)
	case classRefused:
		return reportDegraded(stdout, stderr, configDir, macroID, now, &buffer, result, exitRefused,
			"the coordinator answered and refused this caller (authentication or authorization failure)")
	case classRejected:
		return reportDegraded(stdout, stderr, configDir, macroID, now, &buffer, result, exitRejected,
			"the coordinator answered and declined the request itself — this is NOT a credential problem")
	default: // classUnreachable
		return reportDegraded(stdout, stderr, configDir, macroID, now, &buffer, result, exitUnreachable,
			"the coordinator could not be reached, or answered with a server error")
	}
}

// newIdempotencyKey mints a fresh, random idempotency key: 16 bytes of
// crypto/rand, hex-encoded, one per invocation — matching
// cmd/showmeshctl/cmd_fpp_command.go's own newIdempotencyKey and its own
// reasoning for never reusing a key across two calls. This program mints
// its own independently rather than importing pkg/command for it, for the
// same reason showmeshctl does: see importgraph_test.go.
func newIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// reportLocalError handles an attempt that never reached the coordinator
// at all — a bad credential file, a bad config file, a key-generation
// failure. It still writes a local status record (this IS an attempt, and
// the FPP host is still the only thing that knows why it did not even try
// to contact the coordinator), classed distinctly from the four
// coordinator-conversation classes, and it is never buffered as a prior
// failure: MacroPriorFailureRequest's class enum has no local-error
// member, and a local misconfiguration is not something the coordinator
// can meaningfully be told about later.
func reportLocalError(stdout, stderr io.Writer, configDir, macroID string, now time.Time, cause error) int {
	msg := cause.Error()
	rec := statusRecord{Timestamp: now, MacroID: macroID, Class: classLocalError, Message: msg}
	if err := writeStatus(configDir, rec); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: also failed to write the local status record: %v\n", err)
	}
	_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: %s\n", msg)
	return exitLocalError
}

// reportOK handles a 2xx run submission: refreshes the cached macro
// definition when needed (section 8.1), flushes the failure buffer
// (section 8.3 path 2 — flush happens on 2xx only), and writes the local
// status record.
func reportOK(ctx context.Context, httpClient *http.Client, stdout, stderr io.Writer, configDir, macroID string, coordinatorURL *url.URL, token string, now time.Time, result submitResult) int {
	run := result.Run.Run

	refreshMacroCacheIfStale(ctx, httpClient, stderr, configDir, macroID, coordinatorURL, token, run.MacroRevision, now)

	// Flush: the buffer's contents (if any) were included in THIS
	// request's priorFailures and the coordinator answered 2xx, so per
	// section 8.3's flush rule they are now the coordinator's record, not
	// this host's. Cleared unconditionally, including Dropped, which
	// represents drops that happened before this successful report.
	if err := saveFailureBuffer(configDir, failureBuffer{}); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: run accepted, but failed to clear the local failure buffer: %v\n", err)
	}

	msg := fmt.Sprintf("macro %q accepted as run %s (revision %d)", macroID, run.ID, run.MacroRevision)
	if result.Run.Replay {
		msg += "; this idempotency key was already used, so the ORIGINAL run was returned rather than a new one"
	}
	rec := statusRecord{
		Timestamp: now, MacroID: macroID, Class: classOK,
		HTTPStatus: result.HTTPStatus, RunID: run.ID, Message: msg,
	}
	if err := writeStatus(configDir, rec); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: also failed to write the local status record: %v\n", err)
	}
	_, _ = fmt.Fprintf(stdout, "%s\n", msg)
	return exitOK
}

// refreshMacroCacheIfStale is section 8.1's actual cache refresh: a GET of
// the macro's own configuration object, so the cache carries the
// definition's own label and reason text, not a class with nothing said
// about it. Conditional on purpose — runMacroRevision is the revision the
// run that just executed pinned, and if the cache already holds that same
// revision, the label/classes/reasons on file are still current and a
// fetch would cost a request for no new information. Only fetches when the
// revision moved or nothing is cached yet.
//
// This never affects cmdRun's own return value: the run already succeeded
// (this is only ever called from reportOK), and a cache-refresh failure
// degrades the cache, not the run — whatever was cached before is left
// exactly as it was, and this function logs the failure to stderr rather
// than returning an error a caller might be tempted to act on.
func refreshMacroCacheIfStale(ctx context.Context, httpClient *http.Client, stderr io.Writer, configDir, macroID string, coordinatorURL *url.URL, token string, runMacroRevision int, now time.Time) {
	if cachedRevision, ok := cachedRevisionFor(configDir, macroID); ok && cachedRevision == runMacroRevision {
		return
	}
	cfg, err := fetchMacroConfig(ctx, httpClient, coordinatorURL, token, macroID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: run accepted, but could not refresh the local macro cache (kept whatever was already cached, if anything): %v\n", err)
		return
	}
	if err := updateMacroCache(configDir, macroID, cfg, now); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: run accepted, but failed to write the refreshed local macro cache: %v\n", err)
	}
}

// reportDegraded handles the three section 8.2 degraded classes shared:
// build the operator-facing message (naming the refused/rejected/
// unreachable distinction explicitly and, for a refusal, the cached local
// policy per section 8.1), write the local status record (section 8.3
// path 1 — this is what makes a 403 and a closed port produce visibly
// different local records, and it happens unconditionally, regardless of
// whether buffering below succeeds), and append this outcome to the
// failure buffer for the next successful call to report (section 8.3
// path 2). The buffer is retained, never cleared, on every path through
// this function — the flush rule is "2xx only", and this function is
// never called for a 2xx result.
func reportDegraded(stdout, stderr io.Writer, configDir, macroID string, now time.Time, buffer *failureBuffer, result submitResult, exitCode int, summary string) int {
	detail := problemDetailText(result)

	msg := fmt.Sprintf("macro %q: %s (%s)", macroID, summary, detail)
	if result.Class == classRefused {
		msg += "\n" + localPolicyStatement(configDir, macroID, now)
	}

	rec := statusRecord{
		Timestamp: now, MacroID: macroID, Class: result.Class,
		HTTPStatus: result.HTTPStatus, Message: msg,
	}
	if err := writeStatus(configDir, rec); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: also failed to write the local status record: %v\n", err)
	}

	buffer.append(bufferedFailure{
		MacroObjectID: macroID,
		Class:         result.Class,
		HTTPStatus:    result.HTTPStatus,
		At:            now,
	}, now)
	if err := saveFailureBuffer(configDir, *buffer); err != nil {
		_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: also failed to persist the local failure buffer: %v\n", err)
	}

	_, _ = fmt.Fprintf(stderr, "showmesh-fpp-plugin run: %s\n", msg)
	return exitCode
}

// problemDetailText renders whatever this program learned about why a
// non-2xx attempt failed, in operator-facing terms — the decoded RFC 9457
// problem's title/detail when the body parsed as one, the raw HTTP status
// when it did not, or the transport error when there was no HTTP response
// at all.
func problemDetailText(result submitResult) string {
	if result.Problem != nil {
		if result.Problem.Detail != "" {
			return fmt.Sprintf("%s: %s", result.Problem.Title, result.Problem.Detail)
		}
		return result.Problem.Title
	}
	if result.TransportErr != nil {
		return result.TransportErr.Error()
	}
	if result.HTTPStatus != 0 {
		return fmt.Sprintf("HTTP %d, no further detail in the response", result.HTTPStatus)
	}
	return "no response was received"
}
