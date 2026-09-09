package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// FPPMQTTSecretFileName is the legacy 0600 file the fpp.mqtt broker
// password lived in before it moved into the credentials table (owner
// ruling 2026-09-08, "credentials into SQLite": a database an operator can
// back up as one unit, not a database plus a file somebody forgets). This
// file's only remaining reader is migrateFPPMQTTSecretFileToStore
// (internal/coordinator/fppmqttsync.go), which moves whatever it holds into
// the store once, on a coordinator upgrading from a version that used it,
// and then removes it. Nothing writes to this file anymore.
const FPPMQTTSecretFileName = "fpp-mqtt-broker-password.txt"

func fppMQTTSecretFilePath(dataDir string) string {
	return filepath.Join(dataDir, FPPMQTTSecretFileName)
}

// ReadFPPMQTTPassword returns the legacy file's content. A missing file
// means "nothing to migrate" (present=false), not an error: the steady
// state for a deployment that started after this file retired, or one
// whose migration already ran and removed it.
func ReadFPPMQTTPassword(dataDir string) (password string, present bool, err error) {
	b, err := os.ReadFile(fppMQTTSecretFilePath(dataDir))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("config: read legacy fpp.mqtt secret file: %w", err)
	}
	return string(b), true, nil
}

// ClearFPPMQTTPassword removes the legacy file once its content has been
// moved into the store. A not-exist error is not an error: there is nothing
// left to remove.
func ClearFPPMQTTPassword(dataDir string) error {
	err := os.Remove(fppMQTTSecretFilePath(dataDir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config: delete legacy fpp.mqtt secret file: %w", err)
	}
	return nil
}
