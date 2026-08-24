# SM-209 bench evidence: a fresh FPP 10 player is silent to ShowMesh by default

Captured 2026-08-23 against this bench's `fpp-multisync` project, `fpp-master`
built at the `10.0` tag (`370e62ed7e8c8318da6ee5b01312b8b75082d952`, matching
this repo's pinned commit for that target), on a locally built image
(`sm210-fpp-master:latest`, retagged to this bench's expected
`fpp-multisync-fpp-master:latest` image name -- reused rather than rebuilt
from source, since the machine already had a matching local build).
`showmesh-multisync-probe` was rebuilt from this branch's own tree
(`docker compose build probe`) so the capture below reflects this PR's
diagnostic changes, not a stale probe image.

## Setup: a genuinely fresh FPP 10 install

`docker compose down -v` then `docker compose up -d fpp-master` (fresh
volume, no prior `MultiSyncEnabled`/`MultiSyncUnicast` state). Confirmed
fresh via `GET /api/settings/MultiSyncEnabled` returning no `value` key at
all (RES-002's documented "never written" signature) and `GET
/api/system/info` reporting `"multisync": false`.

Read straight off this fresh container, confirming RES-002's amended claim
directly rather than only from upstream source:

```
GET /api/settings/MultiSyncUnicast
  {..."default":1,...,"value":1}            <- default present AND already 1
GET /api/settings/MultiSyncMulticast
  {...no "default" key, no "value" key...}  <- no default at all
```

`MultiSyncEnabled` was then turned on the way an operator setting up a show
would (`PUT /api/settings/MultiSyncEnabled` with value `"1"`, then a restart
to apply the `"restart":2` setting), leaving the transport checkboxes
untouched at their shipped defaults: `MultiSyncUnicast=1`,
`MultiSyncMulticast`/`MultiSyncBroadcast` unset (off), no
`MultiSyncRemotes`/`MultiSyncExtraRemotes` configured. This is the exact
"operator did the one obvious step and stopped" scenario SM-209 exists for.

## Before: default FPP 10 transport, probe correctly classifies the silence

```
docker compose run --rm probe -duration 25s -quiet -out /captures/sm209-default-before.jsonl
```

A sequence start was issued against `fpp-master` twice during the window
(`GET /api/sequence/sm209-test.fseq/start/0`). Result: **0 datagrams
received.** The probe's summary (this PR's new text):

```
*** NO PACKETS WERE RECEIVED DURING THIS CAPTURE. ***
This is itself a finding, not necessarily an error.

MOST LIKELY EXPECTED CAUSE, if the target is FPP 10: a fresh FPP 10 install
ships with MultiSyncUnicast defaulting to on and MultiSyncMulticast carrying
no default at all (RES-002; upstream www/settings.json at the 10.0 tag), and
FPP 10's automatic unicast targeting only ever selects other FPP instances in
remote mode (supportsUnicast in src/MultiSync.cpp) -- never a third-party
listener such as this probe. A fresh FPP 10 player left at its shipped
defaults will therefore send this probe nothing, on ANY transport, with no
error logged on either side. THIS IS FPP 10 CONFIGURED THE WAY FPP 10 SHIPS,
not a broken listener and not a broken network. ...
```

This is the acceptance evidence: the probe distinguishes "FPP 10 configured
the way FPP 10 ships" from a generic, undifferentiated fault report.

## After: adding the probe as an FPP 10 remote, applied live

```
PUT /api/settings/MultiSyncExtraRemotes  body: "172.21.0.3"   (the probe container's bench-network address)
```

`fppd.log` recorded the change taking effect with **no fppd restart**:
`[Settings] Setting MultiSyncExtraRemotes changed from  to 172.21.0.3`. A
fresh sequence start was then issued and a second capture run:

```
docker compose run --rm probe -duration 30s -quiet -out /captures/sm209-default-after.jsonl
```

Result: **41 datagrams received, all classified `unicast`, 0 malformed, 0
undecodable.** Excerpt:

```
Datagrams received:  41  (decoded_ok=41 not_fppd=0 malformed=0 unknown_type=0)
...
  04:36:18.454  Open  Sequence file="sm209-test.fseq" frame=0 sec=0.000 source=172.21.0.2:50016
  04:36:18.460  Start Sequence file="sm209-test.fseq" frame=0 sec=0.000 source=172.21.0.2:50016
...
Transports that actually delivered a packet during this capture:
  unicast    41 packet(s)
```

This confirms, against a real FPP 10 daemon rather than only upstream
source: adding ShowMesh's address to `MultiSyncExtraRemotes` is a supported,
live (no-restart) way onto an FPP 10 player's sync path, and once added, the
same probe that saw nothing above decodes a clean OPEN/START/SYNC stream.

## What this does and does not establish

This raises confidence that the probe's new diagnostic text is correct
against a real `fppd`, and that the documented operator remedy actually
works on a real FPP 10 daemon. It does not raise RES-002's own verification
level beyond what the amended record states, and it is a single capture on
one container on one machine, not a structured multi-run verification pass.
The observed `Open`/`Start` packets both carried `sec=0.000` (`SecondsElapsed
= 0`), consistent with RES-002's amended note that sequence/media START
hardcodes `secondsElapsed = 0`.
