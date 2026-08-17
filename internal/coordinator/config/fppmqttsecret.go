package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// FPPMQTTSecretFileName holds the fpp.mqtt broker password, outside SQLite
// (ADR-039 decision 7): config_revisions is immutable, so a rotated
// password stored there would leave a permanent copy in revision history.
// Mirrors identity.BootstrapFileName's "written directly to the data
// volume, never to a config_revisions row" pattern for a mutable secret.
const FPPMQTTSecretFileName = "fpp-mqtt-broker-password.txt"

func fppMQTTSecretFilePath(dataDir string) string {
	return filepath.Join(dataDir, FPPMQTTSecretFileName)
}

// WriteFPPMQTTPassword stores password, overwriting any previous value,
// with 0600 permissions and no trailing newline (unlike the bootstrap
// file, this is consumed by the MQTT client, never by `cat`).
func WriteFPPMQTTPassword(dataDir, password string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("config: create data directory %q for fpp.mqtt secret: %w", dataDir, err)
	}
	if err := os.WriteFile(fppMQTTSecretFilePath(dataDir), []byte(password), 0o600); err != nil {
		return fmt.Errorf("config: write fpp.mqtt secret file: %w", err)
	}
	return nil
}

// ReadFPPMQTTPassword returns the currently stored password. A missing
// file means "no password ever set" (present=false), not an error.
func ReadFPPMQTTPassword(dataDir string) (password string, present bool, err error) {
	b, err := os.ReadFile(fppMQTTSecretFilePath(dataDir))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("config: read fpp.mqtt secret file: %w", err)
	}
	return string(b), true, nil
}

// ClearFPPMQTTPassword removes the stored password. A not-exist error is
// not an error: there is nothing to clear.
func ClearFPPMQTTPassword(dataDir string) error {
	err := os.Remove(fppMQTTSecretFilePath(dataDir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: delete fpp.mqtt secret file: %w", err)
	}
	return nil
}

// HasFPPMQTTPassword reports presence only — GET /api/v1/config/fpp.mqtt's
// "passwordSet" answer, never the value (ADR-039 decision 7).
func HasFPPMQTTPassword(dataDir string) (bool, error) {
	_, present, err := ReadFPPMQTTPassword(dataDir)
	return present, err
}
