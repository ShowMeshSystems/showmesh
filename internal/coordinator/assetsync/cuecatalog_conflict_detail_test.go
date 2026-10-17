package assetsync

import (
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func TestCatalogConflictDetailNamesTheResourceWithoutTheDeviceString(t *testing.T) {
	cases := map[config.ShowCueClaim]string{
		{Kind: config.ShowCueClaimKindProgramAudioRoute, Node: "pi-audio-01", Resource: "alsa_output.usb-Focusrite_Scarlett_Solo_USB_Y7ZC1ZB9980C18-00.pro-output-0"}: `Cues bg1 and kpop both use the program audio output on pi-audio-01`,
		{Kind: config.ShowCueClaimKindRenderSurface, Resource: "matrix-main"}:                                                                                         `Cues bg1 and kpop both use the render surface matrix-main`,
		{Kind: config.ShowCueClaimKindLTCOutput, Node: "node-01", Resource: "hw:M4,0"}:                                                                                `Cues bg1 and kpop both use the LTC output on node-01`,
		{Kind: config.ShowCueClaimKindAnnouncementSession, Node: "node-01"}:                                                                                           `Cues bg1 and kpop both use announcements on node-01`,
	}
	for claim, want := range cases {
		got := CatalogConflict{CueA: "bg1", CueB: "kpop", Claim: claim}.Detail()
		if got != want {
			t.Errorf("%s: got %q, want %q", claim.Kind, got, want)
		}
		if strings.Contains(got, "alsa_output") || strings.HasSuffix(got, ".") {
			t.Errorf("%s: leaks the device string or ends in a period: %q", claim.Kind, got)
		}
	}
}
