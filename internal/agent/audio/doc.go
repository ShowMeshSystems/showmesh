// Package audio discovers and probes this node's ALSA audio outputs:
// enumeration is never treated as evidence an output works, and a probe
// reports only what a real gst-launch-1.0 pipeline negotiated reaching
// PLAYING, never what was requested.
//
// gst-device-monitor-1.0 is not in the Debian 13 GStreamer package set
// (bench/audio-node/results/r7_capability_discovery.json), so discovery
// here goes through `aplay -L`/`aplay -l` instead. A container or host with
// no real interface still reports "null" and "default" PCM devices
// (r7_capability_discovery.json) — those are never candidates for a real
// output route (see [Enumerator]).
package audio
