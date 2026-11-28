package api

import (
	"context"
	"errors"
	"fmt"
	"sort"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// nightShowMatchCheckName is the readiness check that a night only ever runs
// against the selected show. No override covers it.
const nightShowMatchCheckName = "night-session-show"

// nightConfigReader is the config read both *store.Store and *store.Tx offer,
// so the same show-match rule decides readiness and the gated commands.
type nightConfigReader interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
	ListConfigObjects(ctx context.Context, kind string) ([]store.ConfigObjectRecord, error)
}

// nightNamedSession is one night.session's id, label and show.
type nightNamedSession struct {
	id, label, show string
}

func (s nightNamedSession) name() string {
	if s.label != "" {
		return s.label
	}
	return s.id
}

// nightCurrentPayload decodes kind/id's current revision into out. found is
// false when the object does not exist, is deleted, or has no revision yet.
func nightCurrentPayload(ctx context.Context, r nightConfigReader, kind, id string, out any) (found bool, err error) {
	obj, err := r.GetConfigObject(ctx, kind, id)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if obj.CurrentRevision == 0 {
		return false, nil
	}
	rev, err := r.GetConfigRevision(ctx, kind, id, obj.CurrentRevision)
	if err != nil {
		return false, err
	}
	if err := jsonUnmarshalStrict(rev.PayloadJSON, out); err != nil {
		return false, fmt.Errorf("api: decode %s/%s: %w", kind, id, err)
	}
	return true, nil
}

// nightSelectedShow returns show.active's show, or "" when none is selected.
func nightSelectedShow(ctx context.Context, r nightConfigReader) (string, error) {
	var p config.ShowActivePayload
	found, err := nightCurrentPayload(ctx, r, config.ShowActiveConfigKind, config.ShowActiveObjectID, &p)
	if err != nil || !found {
		return "", err
	}
	return p.Show, nil
}

// nightShowName is a show's operator name, falling back to its id.
func nightShowName(ctx context.Context, r nightConfigReader, showID string) string {
	var p config.ShowPayload
	if found, err := nightCurrentPayload(ctx, r, config.ShowConfigKind, showID, &p); err == nil && found && p.Name != "" {
		return p.Name
	}
	return showID
}

// nightActiveSession reads night.session.active and the session it names.
// found is false when no session is active or it no longer exists.
func nightActiveSession(ctx context.Context, r nightConfigReader) (nightNamedSession, bool, error) {
	var active config.NightSessionActivePayload
	found, err := nightCurrentPayload(ctx, r, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID, &active)
	if err != nil || !found || active.Session == "" {
		return nightNamedSession{}, false, err
	}
	var p config.NightSessionPayload
	found, err = nightCurrentPayload(ctx, r, config.NightSessionConfigKind, active.Session, &p)
	if err != nil || !found {
		return nightNamedSession{}, false, err
	}
	return nightNamedSession{id: active.Session, label: p.Label, show: p.Show}, true, nil
}

// nightSessionsForShow lists every non-deleted night.session that plays show.
func nightSessionsForShow(ctx context.Context, r nightConfigReader, show string) ([]nightNamedSession, error) {
	objs, err := r.ListConfigObjects(ctx, config.NightSessionConfigKind)
	if err != nil {
		return nil, err
	}
	var out []nightNamedSession
	for _, o := range objs {
		var p config.NightSessionPayload
		found, err := nightCurrentPayload(ctx, r, config.NightSessionConfigKind, o.ID, &p)
		if err != nil {
			return nil, err
		}
		if found && p.Show == show {
			out = append(out, nightNamedSession{id: o.ID, label: p.Label, show: p.Show})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

// nightShowMatch decides whether a night may run against the selected show.
// pinned is the session an open night already holds, nil before Prepare site.
// A coordinator with no selected show has nothing to compare and passes.
func nightShowMatch(ctx context.Context, r nightConfigReader, pinned *nightNamedSession) nightReadinessCheck {
	check := nightReadinessCheck{name: nightShowMatchCheckName, health: nightHealthHealthy()}
	unreadable := func(err error) nightReadinessCheck {
		return nightReadinessCheck{name: nightShowMatchCheckName, health: nightHealthUnknown(), cause: err,
			reason: "The coordinator could not read the selected show or the night session. Run readiness again."}
	}
	selected, err := nightSelectedShow(ctx, r)
	if err != nil {
		return unreadable(err)
	}
	if selected == "" {
		return check
	}
	active, hasActive, err := nightActiveSession(ctx, r)
	if err != nil {
		return unreadable(err)
	}
	selectedName := nightShowName(ctx, r, selected)

	if hasActive && active.show != selected {
		action := fmt.Sprintf("Choose a night session for %s, then run Prepare site.", selectedName)
		if pinned != nil {
			action = fmt.Sprintf("Choose one for %s, then End session and Prepare site.", selectedName)
		}
		check.health = nightHealthFailed()
		check.reason = fmt.Sprintf("Night session %s plays %s, not the selected show %s. %s",
			active.name(), nightShowName(ctx, r, active.show), selectedName, action)
		candidates, err := nightSessionsForShow(ctx, r, selected)
		if err != nil {
			return unreadable(err)
		}
		if len(candidates) == 1 {
			check.candidate = &v1.NightSessionCandidate{ID: candidates[0].id, Label: candidates[0].name()}
		}
		return check
	}
	if pinned != nil && pinned.show != selected {
		check.health = nightHealthFailed()
		check.reason = nightPinnedMismatchReason(pinned.name(), nightShowName(ctx, r, pinned.show), selectedName)
	}
	return check
}

// nightShowMatchRefusal turns a failed or unreadable show-match check into
// the refusal a gated command returns, or nil when the night may proceed.
func nightShowMatchRefusal(check nightReadinessCheck) *v1.Problem {
	if check.health == nightHealthHealthy() {
		return nil
	}
	p := nightNotReadyProblem(check.reason)
	return &p
}

// nightPinnedShowMatchTx is the show-match rule for a command acting on an
// open night: only the session that night holds matters, read inside that
// command's own transaction.
func (h *handlers) nightPinnedShowMatchTx(ctx context.Context, tx *store.Tx, current store.NightSessionRecord) (*v1.Problem, error) {
	payload, err := h.getPinnedNightSessionPayloadTx(ctx, tx, current)
	if err != nil {
		return nil, err
	}
	selected, err := nightSelectedShow(ctx, tx)
	if err != nil {
		return nil, err
	}
	if selected == "" || payload.Show == selected {
		return nil, nil
	}
	pinned := nightNamedSession{id: current.ConfigObjectID, label: payload.Label, show: payload.Show}
	p := nightNotReadyProblem(nightPinnedMismatchReason(pinned.name(), nightShowName(ctx, tx, pinned.show), nightShowName(ctx, tx, selected)))
	return &p, nil
}

func nightPinnedMismatchReason(session, sessionShow, selectedShow string) string {
	return fmt.Sprintf("This night runs %s for %s, not the selected show %s. Run End session, then Prepare site.", session, sessionShow, selectedShow)
}
