package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// defaultConfigDir is the pinned on-host path this plugin's packaging
// repo and this binary agree on, used only when neither --config-dir,
// $SHOWMESH_FPP_PLUGIN_CONFIG_DIR, nor $MEDIADIR (see resolveConfigDir)
// says otherwise. Overridable so the bench never needs to run as the fpp
// user or write under /home/fpp.
const defaultConfigDir = "/home/fpp/media/config/plugin.fpp-showmesh"

// configDirUnderMediaSuffix is appended to $MEDIADIR to build this
// plugin's config directory when FPP itself supplies that variable.
const configDirUnderMediaSuffix = "config/plugin.fpp-showmesh"

// requiredCredentialMode is the exact permission bits the credential file
// must carry. Exact, not "no more permissive than": a file this program
// did not create itself (a cloned SD card, a manual restore, an operator
// "fixing" permissions by hand) can just as easily be too RESTRICTIVE in a
// way that silently breaks reads later, or have a mode that happens to
// look safe but was never actually set by this program's own install path.
// Refusing on anything other than exactly 0600 makes the check mean "this
// is the file this program's own installer wrote," not merely "this file
// looks reasonably private."
const requiredCredentialMode = 0o600

// resolveConfigDir applies the precedence flag > $SHOWMESH_FPP_PLUGIN_CONFIG_DIR
// > $MEDIADIR-derived > pinned literal default. flagValue is empty when
// --config-dir was not passed.
//
// $MEDIADIR sits ahead of the pinned literal specifically because it is
// what FPP itself hands this program, not a guess about where FPP put its
// media root: when FPP forks a plugin's command it passes exactly three
// environment variables — MEDIADIR, FPPDIR, SCRIPTDIR — and nothing else
// (no PATH at all), and MEDIADIR is the authoritative media root on that
// host. Hardcoding the pinned literal ahead of a value FPP is actively
// telling this program would be wrong on any host whose media root was
// ever moved from the default, which is exactly the kind of host-specific
// fact this program has no business assuming when it is not the one
// deciding it.
func resolveConfigDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("SHOWMESH_FPP_PLUGIN_CONFIG_DIR"); v != "" {
		return v
	}
	if mediaDir := os.Getenv("MEDIADIR"); mediaDir != "" {
		return filepath.Join(mediaDir, configDirUnderMediaSuffix)
	}
	return defaultConfigDir
}

func credentialPath(configDir string) string        { return filepath.Join(configDir, "credential") }
func coordinatorConfigPath(configDir string) string { return filepath.Join(configDir, "config.json") }
func statusPath(configDir string) string            { return filepath.Join(configDir, "status.json") }
func failureBufferPath(configDir string) string     { return filepath.Join(configDir, "failures.json") }
func macroCachePath(configDir string) string        { return filepath.Join(configDir, "macro-cache.json") }

// loadCredential reads the bearer token from <config-dir>/credential and
// enforces the mode-0600 rule. The token is never logged, never included
// in an error message, and never returned alongside a nil error unless the
// mode check passed — a caller cannot accidentally use a token from a file
// this function did not first verify.
func loadCredential(configDir string) (string, error) {
	path := credentialPath(configDir)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("credential file %s does not exist; this plugin has not been configured with a coordinator credential", path)
		}
		return "", fmt.Errorf("checking credential file %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("credential path %s is a directory, not a file", path)
	}
	if mode := info.Mode().Perm(); mode != requiredCredentialMode {
		return "", fmt.Errorf(
			"credential file %s has mode %04o; refusing to run until it is exactly 0600 (owner read/write only, "+
				"nothing else) — a credential file readable by anything other than its owner cannot be trusted on "+
				"this host",
			path, mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading credential file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("credential file %s is empty", path)
	}
	return token, nil
}

// pluginConfig is the decoded body of <config-dir>/config.json.
// CoordinatorURL absent, present-and-empty, and present-and-null are three
// distinct decode outcomes: only a present, non-empty, well-formed
// http(s) URL is accepted, so a builder cannot accidentally treat a
// half-written config file as "run against nothing" (which net/http would
// otherwise turn into a confusing relative-URL error deep in the request
// path rather than a clear, early one here).
type pluginConfig struct {
	CoordinatorURL *string `json:"coordinatorUrl"`
}

// loadCoordinatorURL reads and validates <config-dir>/config.json,
// returning the parsed base URL.
func loadCoordinatorURL(configDir string) (*url.URL, error) {
	path := coordinatorConfigPath(configDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("coordinator config file %s does not exist; this plugin has not been configured with a coordinator URL", path)
		}
		return nil, fmt.Errorf("reading coordinator config file %s: %w", path, err)
	}
	var cfg pluginConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing coordinator config file %s: %w", path, err)
	}
	if cfg.CoordinatorURL == nil {
		return nil, fmt.Errorf("coordinator config file %s has no coordinatorUrl key", path)
	}
	if *cfg.CoordinatorURL == "" {
		return nil, fmt.Errorf("coordinator config file %s has an empty coordinatorUrl", path)
	}
	u, err := url.Parse(*cfg.CoordinatorURL)
	if err != nil {
		return nil, fmt.Errorf("coordinator config file %s has an invalid coordinatorUrl %q: %w", path, *cfg.CoordinatorURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("coordinator config file %s: coordinatorUrl %q must use http or https", path, *cfg.CoordinatorURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("coordinator config file %s: coordinatorUrl %q has no host", path, *cfg.CoordinatorURL)
	}
	return u, nil
}
