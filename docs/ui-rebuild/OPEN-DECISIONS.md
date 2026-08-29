# Open decisions for Eric

Questions raised by the operator UI rebuild that I will not answer on my own.
Answer inline under each entry, in any later session. I read this file at the
start of every rebuild seam.

Format: each entry states what is unresolved, why it matters now, the options,
my recommendation, and what the answer unblocks. Answered entries move to
"Settled" at the bottom with the ruling and the date.

---

## Open

### D-001 Density switch: ship it or drop it

**What.** The design-system specimen carries a `data-density="compact"` axis
that swaps row and control height from 34px to 30px. No screen mock uses it, and
the design guide does not mention it in section 1 or section 2.

**Why now.** It is a kit-level element. If it ships, every table row and control
in the kit reads its height from `--row-h` / `--ctrl-h` rather than a literal, and
that shape is hard to retrofit later.

**Options.**
- A. Build the density axis into the kit now, expose it later as a setting.
- B. Build the kit with literal 30 / 34 / 48px heights, no density axis.

**Recommendation.** A. The variables cost nothing now and retrofitting them
across every table later is expensive. No UI to switch it until you ask for one.

**Unblocks.** Kit control and table CSS.

**Ruling:**

---

### D-002 Where the coordinator build string lives

**What.** The specimen's chrome bar shows `v0.9.4 · a3f91c2 · API v1` on the
right side. The Dashboard mock's chrome bar does not: it shows now-playing,
connection, and principal, and the design guide's section 2 list of bar contents
omits the build string entirely.

**Why now.** The chrome bar is kit-level and is on every screen. The guide is
explicit that the bar must not wrap, so anything added to it costs horizontal
room the now-playing group needs.

**Options.**
- A. Follow the guide's section 2 list. No build string in the bar. Put it on
  Settings, or in the Monitor Capabilities facet.
- B. Follow the specimen. Keep the build string in the bar and shrink the
  now-playing group.

**Recommendation.** A. The guide is normative and names the bar's contents
exactly; the specimen was demonstrating the bar's construction, not its final
payload. Losing now-playing room during a show is the worse trade.

**Unblocks.** ChromeBar component.

**Ruling:**

---

## Settled

(none yet)
