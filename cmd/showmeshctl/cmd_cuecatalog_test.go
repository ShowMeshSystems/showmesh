package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestCmdCueCatalogDeployUsageNamesTheEnforcedScope guards against the
// help text drifting from the route's actual gate: the deploy route is
// behind identity.ScopeCueCatalogDeploy ("cuecatalog:deploy", admin only),
// deliberately distinct from asset:write (api/openapi.yaml, cuecatalog
// operations; internal/coordinator/identity/types.go's ScopeCueCatalogDeploy
// doc comment).
func TestCmdCueCatalogDeployUsageNamesTheEnforcedScope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdCueCatalog([]string{"deploy", "-h"}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (flag.ErrHelp); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cuecatalog:deploy") {
		t.Errorf("usage output does not name cuecatalog:deploy:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "asset:write") {
		t.Errorf("usage output wrongly claims asset:write:\n%s", stderr.String())
	}
}
