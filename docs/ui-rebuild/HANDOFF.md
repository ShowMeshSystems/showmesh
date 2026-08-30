# Operator UI rebuild: handoff

Read this, then `REBUILD-PLAN.md`, then `OPEN-DECISIONS.md`.
`CONTROL-INVENTORY.md` is the record of what the deleted UI could do.

## What state the work is in

A branch stack, each PR based on the one before it so its diff is only its own
screen. Base of the stack is `feature/operator-ui-overhaul-2`.

| Branch | PR | Contents |
|---|---|---|
| (on the feature branch, `1a1eed1`) | none | Clear-out, shell, session bands, not-found |
| `ui-rebuild/dashboard` | #196 | Dashboard |
| `ui-rebuild/live-control` | #197 | Live Control |
| `ui-rebuild/show-night` | #198 | Show Night |
| `ui-rebuild/monitor` | #199 | Monitor · Fleet, and the D-005 to D-010 rulings |
| `ui-rebuild/session-states` | #200 | Session states |
| `ui-rebuild/monitor-facets` | #201 | Monitor · Signals, Activity, Capabilities, Manifest |
| `ui-rebuild/shows` | #202 | Shows workspace shell, list, Identity, Playlists |
| `ui-rebuild/shows-tabs` | #203 | Shows · Cues, Assets |
| `ui-rebuild/shows-tabs-2` | #204 | Shows · Presentation, Automation |
| `ui-rebuild/creation-1` | #205 | New show, new playlist, the stale-write guard |

#196 to #200 were green on all ten checks when they were opened; re-check the
later ones rather than assuming. Nothing is merged. Nothing is merged: Eric reviews and merges, and a
session does not merge for him unless he says so in that session.

#199 carries two things rather than one. The rulings commit landed on that
branch by mistake and Eric ruled "leave it" rather than pay for a force-push to
split it.

## What to do next

Eric ruled every open decision on 2026-08-30, so nothing is waiting on him.
`OPEN-DECISIONS.md` opens with the ruling index; `REBUILD-PLAN.md` carries the
order. In short:

1. New action, action editing and new macro, from `Object Creation.dc.html`
   sections 3, 4 and 5. New show and new playlist are done (#205).
2. Node detail.
3. Settings, seven tabs.
4. Access.
5. Resolume Config.
6. The Assets library at `/assets`.
7. The stale-write guard (D-014 B) retrofitted onto the shipped editors.
8. Phase 2: delete the old system and add the check that keeps it deleted.
9. Track C: the API facts D-016 asks for. This one leaves UI-only scope.

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

Traps that have cost time, in the order they will bite again:

- **Every fixture response needs a `ShowMesh-API-Version: 1` header.** Without
  it `client.ts` throws `IncompatibleVersionError` on every call and the whole
  app renders empty with no console error.
- `/events` must carry `latestSeq`. Without it the connection loop never
  completes and the model stays at zero.
- Driving the browser with `location.href` or `location.reload()` from the
  javascript tool races the extension and freezes screenshots for a minute.
  Use the `navigate` tool instead.
- **Stop the fixture coordinator and vite before running the test suite.**
  `src/api/store.test.ts` fails while either is listening: its cases drive a
  real jsdom connection and a local dev server answers requests those cases
  expect to fail. Which case fails moves between runs, which is why it read as
  a flake. With both servers stopped the file passes on its own and in the full
  suite. Verified 2026-08-30 by stopping them and re-running.

## Rules this rebuild runs on

- The mock is the specification. Extract its block list and order, and match it
  exactly. No prepended header, no appended section.
- The goal is not feature parity. A control with no home in a mock goes to
  `OPEN-DECISIONS.md` for Eric with options and a recommendation. Never shove it
  in, and never drop it silently.
- A control the API cannot serve is still built, to the shape the mock draws,
  inert, and marked with the kit's `NotWiredBanner` and `NotWired`. Ruled by
  Eric 2026-08-29 as D-010; it replaces the earlier "state the absence instead"
  rule. Data is the exception: a number, time or row the coordinator never
  reported is still never invented. Only a **missing endpoint** earns the
  not-wired treatment. A control that is inert for any other reason, including
  a design decision not to offer it, is an ordinary disabled control with its
  reason stated beside it. `NotWiredBanner`'s copy says "the coordinator has no
  <endpoint>", so a prose value in `missing` reads as nonsense.
- Anything in `--t-data` or `--t-meta` is a literal identifier: trace it to a
  file before typing it, and never change its case.
- The dashed edge means never-collected, only. A settled state that has not
  happened yet is the kit's `pending` tone.
- A command reports what the coordinator reported. Night commands answer 202:
  say accepted, never done.
- Not being able to read something is not the same fact as that thing being
  absent, stopped or empty. The chrome bar got this wrong until #200.
- `ui/src/domain` gets one module back at a time, reviewed on the way in, when a
  screen needs it. Nothing returns because it used to exist.
- Never write "canonical" or "seam" in anything Eric reads. It is one PR per
  screen.

## What builds these screens

Sonnet subagents build one screen at a time against a written brief; the
orchestrating session reviews the diff rather than the report, fixes what the
review finds, runs the gates itself, and writes the documentation. Reviews have
caught, so far: a second `h1` outside `main`, an `aria-expanded` on a button
controlling no disclosure, a dropped route mapping, a kit element importing app
domain code, and comments over the three-line limit. Read the diff.

## The stale-write guard

`ui/src/domain/save.ts` is the shared path D-014 B asks for, and step 8 of the
plan retrofits the remaining editors onto it.

- `guardedSave` re-reads the object immediately before writing, writes only
  when the revision still matches the one the edit was loaded from, and
  otherwise refuses and names the payload keys that differ.
- `guardedCreate` reads first: a successful read means the id is taken, a 404
  means it is free, and any other read failure refuses. `PUT` on an existing id
  writes a new revision over that object rather than failing, so a create that
  did not read first would silently overwrite.
- `ui/src/screens/StaleWrite.tsx` renders the refusal, so the wording cannot
  drift between editors. Retrofit uses it.

## What is not verified

No screen has been checked against a real coordinator, real nodes, a real FPP
instance, a real Resolume instance, or a real night session. Every browser check
so far was Chrome against the fixture, and no browser check at all was run for
#199's rulings work or #200. Hardware and deployment evidence is Eric's, and
none of it has been collected for this branch.

**The 1280 gate needs a workaround on this machine.** Chrome ignores
`resize_window` here: `window.innerWidth` stays at the display's own width
whatever is requested. Setting `document.documentElement.style.zoom` to
`window.innerWidth / 1280` gives a layout that is 1269 CSS pixels wide, which
is a 1280 viewport minus its scrollbar, and media queries respond to it. That
is what #205's check ran at. Earlier PRs recorded 1372 or 1421 for the same
reason and did not have the workaround.
