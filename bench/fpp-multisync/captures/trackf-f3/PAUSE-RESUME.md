# Track F seam F3: pause/resume capture (F0 gap)

Captured against `showmesh-trackf-fpp` (image `fpp-multisync-fpp-master:latest`,
fresh named volume `showmesh-trackf-fpp-media`) on `http://localhost:8091` —
never against `showmesh-bench-fpp-master` (port 8090). Container created for
this capture and left running for this session's `make test-integration-fpp`
gate; not part of the deployed fleet.

Sequence: start `showmesh-test` (one pause-type item, 120s) one-shot, poll,
issue `GET /api/playlists/pause`, poll for 5s, issue
`GET /api/playlists/resume`, poll, stop.

```
start:            status_name=playing  seconds_elapsed=3   milliseconds_elapsed=3101
pause dispatched: response "Playlist Paused"
+1s:               status_name=paused   seconds_elapsed=10  milliseconds_elapsed=10582
+5s more:          status_name=paused   seconds_elapsed=15  milliseconds_elapsed=15636
resume dispatched: response "Playlist Restarted"
+1s:               status_name=playing  seconds_elapsed=24  milliseconds_elapsed=24496
```

**Finding: `status_name` correctly reports `paused`, but the position
counters (`seconds_elapsed`/`milliseconds_elapsed`) kept advancing in real
time WHILE paused** — they did not freeze. This is the opposite of the
naive assumption ("pause freezes position") a boundary-arming controller
might make.

**Caveat this capture does not resolve:** `showmesh-test`'s single item is
FPP's own `pause`-type playlist entry (a countdown timer, not a rendered
FSEQ sequence) — the seam spec's own seed playlists (`showmesh-test`,
`showmesh-bench-3item`) are the only pre-existing playlists this bench
image ships that are safe to start without uploading new media, and F0's
own real-sequence captures (`trackf-cadence`, `trackf-resting`, etc.) no
longer exist in this fresh container. Whether a genuine FSEQ sequence
item's elapsed-time counter also keeps advancing during a pause is
UNVERIFIED by this capture; it is plausible FPP's pause implementation is
uniform across item types (both read from the same playlist position
clock), but that is an inference from this capture, not a second
independent measurement.

**What this changes in the implementation:** `nightBoundaryContradicted`
(`internal/coordinator/api/nightboundary.go`) invalidates the boundary the
moment `fpp.status` reads `paused`, unconditionally — it does NOT attempt
to compute a "paused-adjusted" boundary by holding the last known
position, because this capture shows that position cannot be trusted to
have frozen. Invalidating (never re-arming automatically) is the
conservative response to what was actually observed here, not merely
what the seam spec assumed in the abstract — but given the above caveat,
this generalizes across item types by inference, not by a second
measurement against a real sequence.
