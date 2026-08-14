# FPP REST captures

The `live_*.json` files are real `/api/fppd/status`, `/api/fppd/ports` and
`/api/system/info` response bodies, captured read-only from three physical FPP
hosts on 2026-08-11 during the Step 5 live probe. They are here because the
invented specification was wrong three times over and these bodies are what
corrected it: three different port-array shapes on one fleet including an empty
array, player and remote reporting structurally different status documents, and
`pixelCount` absent everywhere.

## What was substituted, and what was not

**Deployment identity was replaced on 2026-08-14. Response shape was not
touched.** This repository is public, and the operator's hostnames, addresses
and board serials are permanent once committed while contributing nothing to any
assertion. Nothing here reads back a hostname to prove a fact about FPP.

Substituted, consistently across fixtures, test source and documentation:

| Field | Substituted with |
|---|---|
| Host names | `fpp-player`, `fpp-remote-a`, `fpp-remote-b`, `fpp-ghost` |
| Host addresses | `192.0.2.10`, `.11`, `.12`, `.13` (RFC 5737 TEST-NET-1) |
| Third-party device addresses in warning text | `192.0.2.20`, `192.0.2.21`, and `198.51.100.20` where the real value was on a second subnet, so that difference survives |
| Board serials (`uuid`) | `M1-0000000000000001`, `M1-000000000002`, `M5-000000000000003`, each keeping its original length and prefix so format handling is still exercised |
| Third-party device name | `wled-example` |

Everything else is verbatim: key presence and absence, JSON types including the
number-encoded booleans, array ordering, bank labels, port counts, firmware
version strings, and the deliberate version skew where one remote runs a master
build. `fpp-ghost` and `fpp-remote-a` still share one board serial, because that
is a real property of the capture and is now expressed in synthetic values.

## The rule these files exist to enforce

**A failing test here means the decoder is wrong, not the fixture.** These
bodies are evidence of how FPP actually behaves. Editing one to make a test pass
discards the only thing they are for. If FPP's real behaviour has genuinely
changed, that needs a new capture and a note saying so, not an edit.

`"ma": null` is deliberately not stored in any file here. It is synthesized at
run time by `mutatePortsElementField` in `step5_signals_test.go`, from a real
capture, because no live host returned it and a fixture asserting otherwise
would be claiming an observation that never happened.

## What these captures do not prove

Every `ma` reads 0 because the display was de-energized. That confirms shape and
type and says nothing about whether current telemetry works. RES-011 is not
raised by anything in this directory.
