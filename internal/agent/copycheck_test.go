package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/copycheck"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// copyLedger holds every message this package's tests produce against
// testdata/copy-known-violations.txt.
var copyLedger = copycheck.NewLedger("testdata/copy-known-violations.txt")

func TestMain(m *testing.M) {
	resultObserver = observeResultCopy
	os.Exit(copyLedger.Settle(m.Run()))
}

// A result is keyed by its action, which is fixed in the code, and the field.
func observeResultCopy(result mqttproto.ResultPayload) {
	copyLedger.Observe("result "+result.Action+" reason", result.Reason)
	if result.Evidence == nil {
		return
	}
	if value, err := json.Marshal(result.Evidence.Value); err == nil {
		copyLedger.ObserveJSON("result "+result.Action+" value", value)
	}
}

// The periodic reports have no shared writer, so fakePublisher calls this for
// everything a test publishes. A report is keyed by its topic's fixed part.
func observeReportCopy(topic string, payload []byte) {
	parsed, err := mqttproto.ParseTopic(topic)
	if err != nil || parsed.Kind == mqttproto.TopicKindResult {
		return
	}
	key := "report " + parsed.Kind.String()
	if parsed.Subpath != "" {
		key += " " + parsed.Subpath
	}
	copyLedger.ObserveJSON(key, payload)
}
