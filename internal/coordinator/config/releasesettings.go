package config

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	// ReleaseSettingsConfigKind is the stored kind behind GET and PUT /api/v1/config/release.settings.
	ReleaseSettingsConfigKind = "release.settings"
	// ReleaseSettingsConfigObjectID is the one object id this kind uses.
	ReleaseSettingsConfigObjectID = "default"

	ReleaseSettingsSourceAPI          = "api"
	ReleaseSettingsSourceEnvMigration = "env_migration"
	// ReleaseSettingsSourceEnv labels settings read from the environment that could not be stored.
	ReleaseSettingsSourceEnv = "env"

	// MinReleaseCheckInterval keeps the check inside GitHub's allowance for requests with no login.
	MinReleaseCheckInterval = 5 * time.Minute
	// MaxReleaseCheckInterval is one week, far inside what a time.Duration can hold.
	MaxReleaseCheckInterval = 7 * 24 * time.Hour
)

// ReleaseSettings is whether and how often the coordinator asks GitHub for the newest release.
type ReleaseSettings struct {
	Enabled       bool
	CheckInterval time.Duration
}

// DefaultReleaseSettings is what the coordinator uses before anything is stored: on, every six hours.
func DefaultReleaseSettings() ReleaseSettings {
	return ReleaseSettings{Enabled: true, CheckInterval: defaultReleaseCheckInterval}
}

type releaseSettingsPayload struct {
	Enabled              bool  `json:"enabled"`
	CheckIntervalSeconds int64 `json:"checkIntervalSeconds"`
}

// EncodeReleaseSettingsPayload marshals s into the stored payload shape.
func EncodeReleaseSettingsPayload(s ReleaseSettings) (string, error) {
	b, err := json.Marshal(releaseSettingsPayload{Enabled: s.Enabled, CheckIntervalSeconds: int64(s.CheckInterval / time.Second)})
	if err != nil {
		return "", fmt.Errorf("config: encode release.settings payload: %w", err)
	}
	return string(b), nil
}

// DecodeReleaseSettingsPayload is [EncodeReleaseSettingsPayload]'s inverse.
func DecodeReleaseSettingsPayload(raw string) (ReleaseSettings, error) {
	var p releaseSettingsPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return ReleaseSettings{}, fmt.Errorf("config: decode release.settings payload: %w", err)
	}
	return ReleaseSettings{Enabled: p.Enabled, CheckInterval: ReleaseCheckIntervalFromSeconds(p.CheckIntervalSeconds)}, nil
}

// ReleaseCheckIntervalFromSeconds converts without overflowing: a value outside
// the accepted range becomes one second past it, which [ReleaseSettingsProblem] refuses.
func ReleaseCheckIntervalFromSeconds(seconds int64) time.Duration {
	if seconds > int64(MaxReleaseCheckInterval/time.Second) {
		return MaxReleaseCheckInterval + time.Second
	}
	if seconds < 0 {
		return -time.Second
	}
	return time.Duration(seconds) * time.Second
}

// ReleaseSettingsProblem is the operator's one line for settings that cannot be saved, or "" when they can.
func ReleaseSettingsProblem(s ReleaseSettings) string {
	if s.CheckInterval < MinReleaseCheckInterval {
		return fmt.Sprintf("The check interval is too short. Set it to %d seconds or more.", int64(MinReleaseCheckInterval/time.Second))
	}
	if s.CheckInterval > MaxReleaseCheckInterval {
		return fmt.Sprintf("The check interval is too long. Set it to %d seconds or less.", int64(MaxReleaseCheckInterval/time.Second))
	}
	return ""
}
