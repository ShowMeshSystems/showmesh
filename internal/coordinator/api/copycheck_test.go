package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/copycheck"
)

// copyLedger holds every message this package's tests produce against
// testdata/copy-known-violations.txt. TestMain settles it.
var copyLedger = copycheck.NewLedger("testdata/copy-known-violations.txt")

func init() {
	responseObserver = observeResponseCopy
	copyLedger.Deprecate("session.readiness.checks[].reason")
}

// A Problem's detail is keyed by its type and the function that wrote it. Any
// other body is keyed by its Go type and the path of the field.
func observeResponseCopy(body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return
	}
	if problem, ok := body.(v1.Problem); ok {
		key := "problem " + strings.TrimPrefix(problem.Type, problemBaseURI)
		copyLedger.Observe(key+" in "+problemWriter(), problem.Detail)
		copyLedger.ObserveJSON(key, encoded)
		return
	}
	copyLedger.ObserveJSON(fmt.Sprintf("%T", body), encoded)
}

// problemWriter names the function that called writeProblem: its last named
// part, so a closure's number never reaches a key.
func problemWriter() string {
	pcs := make([]uintptr, 16)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	sawWriteProblem := false
	for {
		frame, more := frames.Next()
		name := lastNamedPart(frame.Function)
		if sawWriteProblem {
			return name
		}
		sawWriteProblem = name == "writeProblem"
		if !more {
			return "unknown"
		}
	}
}

var closureSuffix = regexp.MustCompile(`(\.(func)?\d+|\[\.\.\.\])+$`)

func lastNamedPart(function string) string {
	name := closureSuffix.ReplaceAllString(function, "")
	return name[strings.LastIndex(name, ".")+1:]
}
