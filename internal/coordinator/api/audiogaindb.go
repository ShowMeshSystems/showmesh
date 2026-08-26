package api

import (
	"fmt"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file is THE coordinator's decibel boundary for the two
// operator-facing gain commands. An operator sends
// params.gainDb / params.targetGainDb; everything below this line —
// the coordinator-to-agent wire, pkg/audio.Gain, the agent, and the
// engine — stays a linear amplitude multiplier. The arithmetic itself
// lives in pkg/audio and is not repeated here.
//
// The night-session controller reaches internal/agent through
// executeAudioSessionDispatch directly, not through this file's caller,
// and converts its own already-decibel configuration with the same
// pkg/audio helpers. So there is exactly one conversion per path and
// never two on one value.

// audioGainDbFields names, per action, the decibel parameter an operator
// sends and the linear parameter the node receives.
var audioGainDbFields = map[string]struct{ dbKey, linearKey string }{
	"audio.gain.set":  {dbKey: "gainDb", linearKey: "gain"},
	"audio.gain.fade": {dbKey: "targetGainDb", linearKey: "targetGain"},
}

// convertAudioGainParamsToLinear replaces action's decibel parameter with
// the linear one the node's own operation expects, in place. A nil return
// means params is ready to dispatch.
//
// The pre-change linear parameter name is refused rather than accepted:
// the two units overlap numerically, so a client still sending
// {"gain": 0.5} means a halving and would otherwise be dispatched as a
// half-decibel lift — audibly wrong and silently so.
func convertAudioGainParamsToLinear(action string, params map[string]any) *v1.Problem {
	fields, ok := audioGainDbFields[action]
	if !ok {
		return nil
	}

	if _, present := params[fields.linearKey]; present {
		p := invalidParameterProblem(fmt.Sprintf(
			"params.%s was a linear amplitude multiplier and no longer exists; send params.%s instead, in decibels (0 dB is unity, %v dB is silence)",
			fields.linearKey, fields.dbKey, pkgaudio.SilenceFloorDb))
		return &p
	}

	raw, present := params[fields.dbKey]
	if !present {
		p := invalidParameterProblem(fmt.Sprintf(
			"params.%s is required, in decibels (0 dB is unity, %v dB is silence)",
			fields.dbKey, pkgaudio.SilenceFloorDb))
		return &p
	}
	db, ok := raw.(float64)
	if !ok {
		p := invalidParameterProblem(fmt.Sprintf("params.%s must be a JSON number in decibels, got %T", fields.dbKey, raw))
		return &p
	}
	if db > audioGainDbMax {
		p := invalidParameterProblem(fmt.Sprintf(
			"params.%s is in decibels and must not exceed %v dB: a larger value is a typo, not a level",
			fields.dbKey, audioGainDbMax))
		return &p
	}

	delete(params, fields.dbKey)
	params[fields.linearKey] = float64(pkgaudio.GainFromDb(db))
	return nil
}

// audioGainDbMax is the same +12 dB typo guard audio.settings puts on its
// own ceiling: not a tuned headroom figure, just the point past which a
// number is far more likely to be a mistake than an intended level.
const audioGainDbMax = 12.0
