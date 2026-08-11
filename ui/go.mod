// This is not a real Go module: ui/ contains no Go source of its own.
// Its only purpose is to mark a nested-module boundary so that the root
// module's `go build ./...`, `go vet ./...`, `go test ./...`,
// `gofmt -l .`, and `golangci-lint run ./...` all stop descending here,
// exactly as they already stop at "vendor" directories. Without it, those
// commands walk into ui/node_modules once `npm install` has run there and
// can pick up stray .go files some npm packages happen to ship (found
// during Step 4: a real file under
// node_modules/flatted/golang/pkg/flatted was picked up by `go test ./...`
// and reported as a package with no test files). That is a real,
// reproducible contamination of the Go toolchain, not a hypothetical one,
// and this file is the standard Go fix for it, not a placeholder or a
// stub for future UI-side Go code — none is planned (CLAUDE.md constraint
// 7: the Operator UI is TypeScript, full stop).
module github.com/showmeshsystems/showmesh/ui/_unused

go 1.25
