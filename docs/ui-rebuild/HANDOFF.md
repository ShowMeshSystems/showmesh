# Operator UI rebuild: handoff

Written 2026-08-29 at the end of the first build session. Read this, then
`REBUILD-PLAN.md`, then `OPEN-DECISIONS.md`. `CONTROL-INVENTORY.md` is the
record of what the deleted UI could do.

## What state the work is in

Branch stack, each PR based on the one before it so its diff is only its own
screen. Base of the stack is `feature/operator-ui-overhaul-2`.

| Branch | PR | Screen |
|---|---|---|
| (on the feature branch, `1a1eed1`) | none | Clear-out, shell, session bands, not-found |
| `ui-rebuild/dashboard` | #196 | Dashboard |
| `ui-rebuild/live-control` | #197 | Live Control |
| `ui-rebuild/show-night` | #198 | Show Night |
| `ui-rebuild/monitor` | #199 | Monitor · Fleet |

#196 was green on all ten checks. #197, #198 and #199 were still running when
the session ended: check them before doing anything else.

Nothing has been merged. Eric reviews and merges; do not merge for him unless
he says so in that session.

## The working tree is not clean

Two uncommitted edits sit on `ui-rebuild/monitor`, both removing a hardcoded
`erbartos` that was mine, not the app's:

- `ui/scripts/dev-fixture-server.mjs`: the fixture principal, the night-session
  authorization, two event sources and a config author now read
  `fixture-operator`.
- `ui/src/kit/Specimen.tsx`: the chrome-bar specimen now reads `operator`.

Commit or discard them deliberately. The application itself never hardcoded a
name: `app/Layout.tsx` reads `model.session?.principal?.name`.

## What to do next

Eric's ruling at the end of the session: **the login screen comes before the
remaining Monitor facets.** He could not sign in to look at the rebuild, which
makes every later screen unverifiable for him. The plan's order otherwise
stands.

1. **Session States** (`Session States.dc.html`). The signed-out and bootstrap
   bands already exist in `app/SessionBand.tsx` and work, but they are the
   minimum: name, password, machine token. The mock adds what is missing: the
   device-name field (required, and the thing that lets an administrator revoke
   one device in Access), the "Use a token instead" and "Clear stored token"
   affordances, the two sign-in refusals (`proxy` and `rate-limit`, both already
   distinguished by `domain/session.ts`'s `describeApiError`), the connecting
   state, and the never-collected blanking plate that explains why every
   destination is empty. `views/NotFound` is already rebuilt; the mock's fuller
   old-address table is not.
2. Monitor · Signals, Activity, Capabilities, Manifest.
3. Shows workspace, five tabs. Then Node detail, Settings, Access, Resolume
   Config, then the Phase 2 deletion check.

## Five decisions are waiting for Eric

D-005 through D-009 in `OPEN-DECISIONS.md`, all filed with options and a
recommendation. None blocks a screen. Read them at the start of every screen:
a ruling there overrides the guide.

## How to run and verify

A screenshot of an empty page proves nothing, so the visual gate needs data.

```
node ui/scripts/dev-fixture-server.mjs                 # fixture coordinator, :8099
SHOWMESH_DEV_API=http://localhost:8099 npx vite        # :5173
```

To point the same dev server at a real coordinator, set `SHOWMESH_DEV_API` to
its address instead. The proxy is dev-only; the built image still routes through
nginx.

**The fixture is a fixture.** Its numbers, names and times are invented to match
the mock's scenario. Nothing it shows is evidence about the real fleet, and a
screenshot of it is never hardware or deployment verification. Say so in a PR.

Traps that cost time this session, in the order they will bite again:

- **Every fixture response needs a `ShowMesh-API-Version: 1` header.** Without
  it `client.ts` throws `IncompatibleVersionError` on every call and the whole
  app renders empty with no console error.
- `/events` must carry `latestSeq`. Without it the connection loop never
  completes and the model stays at zero.
- Driving the browser with `location.href` or `location.reload()` from the
  javascript tool races the extension and freezes screenshots for a minute.
  Use the `navigate` tool instead.
- `src/api/store.test.ts` "keepalive comments are inert" fails occasionally
  under a full parallel run and passes alone. It is timing-sensitive, not a
  regression from this work. Report it, do not chase it.

## Rules this rebuild runs on

- The mock is the specification. Extract its block list and order, and match it
  exactly. No prepended header, no appended section.
- The goal is not feature parity. A control with no home in a mock goes to
  `OPEN-DECISIONS.md` for Eric with options and a recommendation. Never shove it
  in, and never drop it silently.
- Never invent a control the API cannot serve. State the absence instead, and
  file the question. This has already happened three times: the installation
  E-stop, firing an announcement cue, and predicting night-command validity.
- Anything in `--t-data` or `--t-meta` is a literal identifier: trace it to a
  file before typing it, and never change its case.
- The dashed edge means never-collected, only. A settled state that has not
  happened yet is the kit's `pending` tone.
- A command reports what the coordinator reported. Night commands answer 202:
  say accepted, never done.
- `ui/src/domain` gets one module back at a time, reviewed on the way in, when a
  screen needs it. Nothing returns because it used to exist.
- Do not say "seam" in anything Eric reads. It is one PR per screen.

## What is not verified

No screen has been checked against a real coordinator, real nodes, a real FPP
instance, a real Resolume instance, or a real night session. Every browser check
was Chrome against the fixture, in dark, light and contrast, at 1372 or 1421 CSS
pixels rather than exactly 1280. Hardware and deployment evidence is Eric's, and
none of it has been collected for this branch.
