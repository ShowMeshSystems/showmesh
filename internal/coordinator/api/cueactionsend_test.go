package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/sendsignal"
)

func TestDispatchFPPCommandSignalsSentOnlyWhenFPPAccepted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   int32
	}{{"accepted", http.StatusOK, 1}, {"refused by FPP", http.StatusInternalServerError, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			fppSrv, _ := newFakeFPPCommandServer(t, tc.status, "Stopped")
			setup := newFPPCommandTestSetup(t, fixedClock(testNow))
			setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
			h := &handlers{
				deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger(),
				fppCommandConfirmDeadline: 50 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
			}
			var sent atomic.Int32
			ctx := sendsignal.WithHook(context.Background(), func() { sent.Add(1) })
			if _, problem, err := h.dispatchFPPCommand(ctx, testNow, FPPCommandInput{
				InstanceID: "bench-fpp", Action: "stopPlaylist", IdempotencyKey: "send-signal-" + tc.name,
				Issuer: FPPCommandIssuer{PrincipalID: "system:test"},
			}); problem != nil || err != nil {
				t.Fatalf("dispatch: problem %+v, err %v", problem, err)
			}
			if got := sent.Load(); got != tc.want {
				t.Fatalf("send hook fired %d times, want %d", got, tc.want)
			}
		})
	}
}
