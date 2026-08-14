// Command showmesh-fpp-plugin is a dedicated operational binary that runs
// ON an FPP host, invoked by FPP's own command mechanism (a script plugin
// registered through commands/descriptions.json — per RES-015 section 7.2,
// not a C++ plugin), whose job is to fire a ShowMesh macro and to make what
// happened legible on the FPP host itself, without needing the coordinator
// to be reachable to read that record.
//
// This is a deliberately separate binary from showmeshctl. showmeshctl is
// the small emergency/admin path for when the UI or coordinator is
// unavailable; this binary's runtime behaviour (credential handling,
// classification of a refusal versus an outage, the local status record,
// the buffered-failure report) is specific to running unattended on an FPP
// host and does not belong bolted onto that CLI.
//
// Like showmeshctl, this program is the API's independent client: it may
// never import a coordinator package (internal/coordinator/... or a
// package a coordinator package's own wire types live in), enforced by
// importgraph_test.go, modelled on cmd/showmeshctl/importgraph_test.go.
// This program decodes the wire contract on its own terms so a rename on
// the server side breaks this build rather than silently drifting.
//
// # Commands
//
//	showmesh-fpp-plugin run <macroId>   fire a macro run
//	showmesh-fpp-plugin status          print the local status record
//	showmesh-fpp-plugin version         print this binary's version
//
// # The credential
//
// The credential is read from a mode-0600 file this plugin owns
// (<config-dir>/credential) and is never a command argument, a URL query
// parameter, or set in a child process's environment. FPP publishes every
// command execution, with its arguments, in cleartext to MQTT
// "command/run", so a token passed as an argument would be broadcast on
// every invocation of this program — a defect this program's own design
// exists to make structurally impossible, not merely discouraged. Refusing
// to run when the credential file's mode is not exactly 0600 is this
// program's own defence against a defect that would otherwise be invisible
// until read from the broker.
//
// # What this program does NOT prove
//
// This program is developed and tested against a bench FPP instance, never
// against a real installation. That licenses the plugin's own mechanism —
// how it classifies a response, how it writes its local record, how it
// buffers a degraded outcome — and says nothing about on-host install,
// filesystem permissions, packaging, or cross-version compatibility on a
// real FPP host. No comment, log line, or string in this package claims
// otherwise.
package main
