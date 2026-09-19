package api

import (
	"context"
	"sync"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// weatherDelayNotSavedMessage is the operator sentence for a start whose
// active state could not be stored.
const weatherDelayNotSavedMessage = "The weather delay could not be saved, so it will not survive a coordinator restart. Everything was stopped and the alert was sent. Press start again."

// WeatherDelayStateKeeper wraps the stored weather delay state. When
// storing an active state fails, it keeps that state in memory, so every
// hold in this process still sees it, until SaveUnsaved stores it.
type WeatherDelayStateKeeper struct {
	inner   WeatherDelayStore
	writeMu sync.Mutex
	mu      sync.Mutex
	unsaved *store.WeatherDelayStateRecord
}

// NewWeatherDelayStateKeeper wraps inner.
func NewWeatherDelayStateKeeper(inner WeatherDelayStore) *WeatherDelayStateKeeper {
	return &WeatherDelayStateKeeper{inner: inner}
}

// GetWeatherDelayState returns the unsaved active state when there is one,
// otherwise the stored state.
func (k *WeatherDelayStateKeeper) GetWeatherDelayState(ctx context.Context) (store.WeatherDelayStateRecord, error) {
	k.mu.Lock()
	unsaved := k.unsaved
	k.mu.Unlock()
	if unsaved != nil {
		return *unsaved, nil
	}
	return k.inner.GetWeatherDelayState(ctx)
}

// SetWeatherDelayState stores rec and drops any unsaved state. A failed
// write of an active state is kept in memory in its place.
func (k *WeatherDelayStateKeeper) SetWeatherDelayState(ctx context.Context, rec store.WeatherDelayStateRecord) error {
	k.writeMu.Lock()
	defer k.writeMu.Unlock()
	err := k.inner.SetWeatherDelayState(ctx, rec)
	k.mu.Lock()
	k.unsaved = nil
	if err != nil && rec.Active {
		kept := rec
		k.unsaved = &kept
	}
	k.mu.Unlock()
	return err
}

// SaveUnsaved retries storing the unsaved active state. nil means nothing
// is left unsaved.
func (k *WeatherDelayStateKeeper) SaveUnsaved(ctx context.Context) error {
	k.writeMu.Lock()
	defer k.writeMu.Unlock()
	k.mu.Lock()
	unsaved := k.unsaved
	k.mu.Unlock()
	if unsaved == nil {
		return nil
	}
	if err := k.inner.SetWeatherDelayState(ctx, *unsaved); err != nil {
		return err
	}
	k.mu.Lock()
	k.unsaved = nil
	k.mu.Unlock()
	return nil
}
