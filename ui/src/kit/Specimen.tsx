import { useState } from 'react'
import {
  BlankingPlate,
  Button,
  ButtonRow,
  ButtonRule,
  Callout,
  Choice,
  ChoiceRow,
  ChromeBar,
  ChromeProgress,
  ClockSkewStrip,
  ConnectionPill,
  DefinitionStrip,
  Field,
  FieldGrid,
  Freshness,
  Input,
  Notice,
  NotWired,
  NotWiredBanner,
  RailBadge,
  RuledStrip,
  Segmented,
  Select,
  StatusPair,
  Table,
  TableWrap,
} from './index'
import './styles/index.css'
import './styles/specimen.css'

type Theme = 'dark' | 'light' | 'contrast'
type Density = 'default' | 'compact'

const THEMES = [
  { value: 'dark' as const, label: 'Dark' },
  { value: 'light' as const, label: 'Light' },
  { value: 'contrast' as const, label: 'High contrast' },
]

const DENSITIES = [
  { value: 'default' as const, label: 'Default' },
  { value: 'compact' as const, label: 'Compact' },
]

const DEPTHS = [
  { token: '--sunken', use: 'Wells, inset tracks, code' },
  { token: '--bg', use: 'Page canvas' },
  { token: '--surface', use: 'Rail, top bar, forms, tables' },
  { token: '--raised', use: 'State blocks, elevated context' },
]

const ACCENT = [
  { token: '--accent-bg', use: 'Selected row, active tab wash' },
  { token: '--accent-border', use: 'Quiet edges, dividers in accent context' },
  { token: '--accent', use: 'Rest: primary action, links, current' },
  { token: '--accent-hover', use: 'Pointer over' },
  { token: '--accent-active', use: 'Pressed, and the focus ring base' },
]

const TONES = [
  { tone: 'good' as const, word: 'Healthy', token: '--good-fg / bg / border', use: 'Observed, current, within threshold' },
  { tone: 'warn' as const, word: 'Degraded', token: '--warn-fg / bg / border', use: 'Stale, restarting, superseded, conflicted' },
  { tone: 'bad' as const, word: 'Failed', token: '--bad-fg / bg / border', use: 'Observed failure with a stated reason' },
  { tone: 'unknown' as const, word: 'Unknown', token: '--unk-fg / bg / border', use: 'Unobserved, never collected, signed out' },
]

const TYPE_ROLES = [
  { cls: 'sm-display', sample: 'Run of Show', role: 'display', spec: '25 / 1.15 / 700 / −.02em · page titles only' },
  { cls: 'sm-heading', sample: 'Presentation path', role: 'heading', spec: '20 / 1.2 / 600 / −.015em · section h2' },
  { cls: 'sm-subhead', sample: 'Program output routing', role: 'subhead', spec: '15 / 1.3 / 600 · field-group h3' },
  { cls: 'sm-body', sample: 'FPP remains authoritative for its own schedule, playlist order, and playhead.', role: 'body', spec: '14 / 1.5 / 400 · prose, labels, controls' },
  { cls: 'sm-small sm-muted', sample: 'Helper text appears only when it changes the decision.', role: 'small', spec: '12.5 / 1.45 / 400 · helper, detail, captions' },
  { cls: 'sm-meta sm-faint', sample: 'Freshness · Revision · Source', role: 'meta', spec: '11 / 1.35 / 600 mono / .09em caps · labels' },
  { cls: 'sm-data', sample: 'rev 47 · 21:04:11.882 · a3f91c2', role: 'data', spec: '13 / 1.4 / 500 mono tabular · values, IDs' },
]

const STRIPS = [
  { absence: 'loading' as const, label: 'Loading', fact: 'Waiting for the first snapshot', detail: 'An empty list here would not be evidence of an empty fleet.' },
  { absence: 'empty' as const, label: 'Empty', fact: 'No cues authored yet', detail: <>A cue binds a transition step to a playlist item. <a href="#specimen-states">Add the first cue</a></> },
  { absence: 'stale' as const, label: 'Stale · 4 m 12 s', fact: 'Pipeline state pre-dates the current stream gap', detail: <>Stale is unknown, never healthy. <a href="#specimen-states">Re-fetch snapshot</a></> },
  { absence: 'failed' as const, label: 'Failed · retry', fact: 'Discovery did not complete', detail: <>The agent on <span className="sm-data">node-04</span> did not answer in 5 s. <a href="#specimen-states">Run discovery again</a></> },
  { absence: 'unavailable' as const, label: 'Unavailable', fact: 'Source does not support this field', detail: 'FPP 7.x does not report per-output current. Nothing to retry.' },
  { absence: 'unobserved' as const, label: 'Unobserved', fact: <>No collector has ever returned <span className="sm-data">audio.route.owner</span></>, detail: 'Distinct from a failed read. Nothing to retry.' },
  { absence: 'signedOut' as const, label: 'Signed out', fact: 'Reads are open; writes are not', detail: <>You can see this show. <a href="#specimen-states">Sign in</a> to change it.</> },
  { absence: 'noPermission' as const, label: 'No permission', fact: <>Missing scope <span className="sm-data">config:write</span></>, detail: 'A refusal from a healthy coordinator, not a connection problem.' },
]

const ROWS = [
  { node: 'Barn roof', signal: 'surface.pipeline.state', tone: 'good' as const, word: 'Playing', fresh: 'Current', stale: false, at: '21:04:11' },
  { node: 'Driveway arch', signal: 'surface.pipeline.state', tone: 'warn' as const, word: 'Restarting', fresh: 'Stale · 4 m 12 s', stale: true, at: '20:59:59' },
  { node: 'Front hedge', signal: 'audio.route.owner', tone: 'unknown' as const, word: 'Unobserved', fresh: 'Never collected', stale: false, at: 'not collected' },
  { node: 'Pump house', signal: 'node.controlplane.state', tone: 'bad' as const, word: 'Offline', fresh: 'Last will received', stale: false, at: '20:41:07' },
]

const RAIL_STATES = [
  { cls: 'sm-spec-rail__row sm-spec-rail__row--current', name: 'Current', note: 'accent edge + wash + weight' },
  { cls: 'sm-spec-rail__row sm-spec-rail__row--hover', name: 'Hover', note: 'raised + neutral edge' },
  { cls: 'sm-spec-rail__row sm-spec-rail__row--focus', name: 'Focus', note: 'inset focus ring' },
  { cls: 'sm-spec-rail__row sm-spec-rail__row--off', name: 'Unavailable', note: 'no scope for this destination' },
]

function SpecSection({ number, title, detail, id, children }: { number: string; title: string; detail?: string; id: string; children: React.ReactNode }) {
  return (
    <section className="sm-spec-section" aria-labelledby={id}>
      <p className="sm-eyebrow">{number}</p>
      <h2 className="sm-section__title" id={id}>{title}</h2>
      {detail !== undefined && <p className="sm-section__detail">{detail}</p>}
      {children}
    </section>
  )
}

/**
 * The element kit, rendered whole. This page is the acceptance gate for the
 * visual language and the reference for adding an element the screen mocks
 * did not cover: compose from what is here, or it does not ship.
 */
export function Specimen() {
  const [theme, setTheme] = useState<Theme>('dark')
  const [density, setDensity] = useState<Density>('default')

  return (
    <div className="sm sm-spec" data-theme={theme} data-density={density}>
      <header className="sm-spec-head">
        <div className="sm-spec-head__left">
          <span className="sm-brand" aria-hidden="true">SM</span>
          <span className="sm-subhead">ShowMesh Operator</span>
          <span className="sm-meta sm-faint">Element kit</span>
        </div>
        <div className="sm-spec-head__right">
          <Segmented label="Theme" value={theme} options={THEMES} onChange={setTheme} />
          <Segmented label="Density" value={density} options={DENSITIES} onChange={setDensity} />
        </div>
      </header>

      <div className="sm-spec-body">
        <section className="sm-spec-section sm-spec-section--lead" aria-labelledby="specimen-lead">
          <p className="sm-eyebrow sm-eyebrow--accent">Foundation</p>
          <h1 className="sm-page__title" id="specimen-lead">A restrained, technical system for show-time decisions</h1>
          <p className="sm-spec-lead">
            Dark graphite is the show-time default. Light is a daylight setup mode. High contrast is a deliberate
            operator setting for gloves and outdoor night work, never inferred. One blue-green accent carries
            selection, focus, links, and the single primary action; green, amber, and red are reserved for
            labelled evidence.
          </p>
          <DefinitionStrip
            items={[
              { term: 'Typeface', value: 'Archivo', detail: 'Grotesque, tight apertures, holds at 11px' },
              { term: 'Metadata face', value: 'JetBrains Mono', detail: 'IDs, timestamps, evidence labels' },
              { term: 'Accent', value: <span className="sm-data sm-spec-accent-value">oklch(.845 .112 181)</span>, detail: 'Hue held from the current build' },
              { term: 'Rhythm', value: <span className="sm-data">4 px</span>, detail: '4 · 8 · 12 · 16 · 24 · 32 · 48' },
            ]}
          />
        </section>

        <SpecSection number="01 · Surfaces" id="specimen-surfaces" title="Four depths, and hairlines do the grouping" detail="Panels signal a meaningfully different context, state, or confirmation. An ordinary group of fields gets a heading and a divider, not a card.">
          <div className="sm-spec-swatches">
            {DEPTHS.map((depth) => (
              <div key={depth.token} className="sm-spec-swatch">
                <div className="sm-spec-swatch__chip" style={{ background: `var(${depth.token})` }} />
                <div>
                  <div className="sm-data">{depth.token}</div>
                  <div className="sm-small sm-muted">{depth.use}</div>
                </div>
              </div>
            ))}
            <div className="sm-spec-swatch">
              <div className="sm-spec-swatch__chip sm-spec-swatch__chip--rules" />
              <div>
                <div className="sm-data">--border / --border-strong</div>
                <div className="sm-small sm-muted">Hairline · control edge</div>
              </div>
            </div>
          </div>
        </SpecSection>

        <SpecSection number="02 · Accent ramp" id="specimen-accent" title="One accent, five stops, real interaction states" detail="Five named stops, not one flat accent with a brightness filter for hover. A filter reads as an accident under high contrast.">
          <div className="sm-spec-ramp">
            {ACCENT.map((stop) => (
              <div key={stop.token} className="sm-spec-ramp__cell">
                <div className="sm-spec-ramp__chip" style={{ background: `var(${stop.token})`, borderColor: stop.token === '--accent-bg' ? 'var(--accent-border)' : 'transparent' }} />
                <div className="sm-data sm-spec-ramp__name">{stop.token}</div>
                <div className="sm-small sm-muted">{stop.use}</div>
              </div>
            ))}
          </div>
        </SpecSection>

        <SpecSection number="03 · Evidence colour" id="specimen-status" title="Status is a labelled pair, never a colour alone" detail="Every status surface renders a glyph and a word as well as a fill. All four tones sit at matched lightness and chroma, so amber never reads louder than green.">
          <div className="sm-grid sm-grid--auto sm-spec-tones">
            {TONES.map((entry) => (
              <div key={entry.tone} className={`sm-evidence sm-evidence--${entry.tone}`}>
                <StatusPair tone={entry.tone} label={entry.word} size="lg" />
                <div className={`sm-data sm-spec-tone__token sm-spec-tone__token--${entry.tone}`}>{entry.token}</div>
                <div className="sm-small sm-muted sm-spec-tone__use">{entry.use}</div>
              </div>
            ))}
          </div>
          <Callout>
            A dashed edge belongs only to an explicit unobserved state block, never a generic unknown status.
            Absent evidence must not be able to borrow the shape of a settled state, in any theme, at any zoom.
          </Callout>
        </SpecSection>

        <SpecSection number="04 · Type scale" id="specimen-type" title="Seven roles, and mono is an accent">
          <div className="sm-spec-type">
            {TYPE_ROLES.map((role) => (
              <div key={role.role} className="sm-spec-type__row">
                <div className={role.cls}>{role.sample}</div>
                <div>
                  <div className="sm-data">{role.role}</div>
                  <div className="sm-small sm-muted">{role.spec}</div>
                </div>
              </div>
            ))}
          </div>
        </SpecSection>

        <SpecSection number="05 · Controls" id="specimen-controls" title="One primary action, and secondary means secondary" detail="34px on a pointer, 48px minimum where the shell is touched with gloves. Destructive controls sit physically apart from the save path.">
          <ButtonRow>
            <Button variant="primary">Save changes</Button>
            <Button>Test connection</Button>
            <Button variant="quiet">Cancel</Button>
            <ButtonRule />
            <Button variant="danger">Revoke credential</Button>
            <Button disabled={true}>Apply · needs scope</Button>
          </ButtonRow>
          <div className="sm-spec-gloved">
            <Button variant="primary" size="gloved">Start night</Button>
            <Button size="gloved">Run readiness</Button>
            <span className="sm-small sm-muted">48px: transport, lifecycle commands, macro Run, sign-in and bootstrap fields.</span>
          </div>
          <FieldGrid>
            <Field label="FPP endpoint address" help="Reachable from the coordinator, not the browser.">
              {(props) => <Input defaultValue="10.20.0.14" {...props} />}
            </Field>
            <Field label="Program output group" help="Resolved from server inventory.">
              {(props) => (
                <Select defaultValue="left" {...props}>
                  <option value="left">House left · 2 ch free</option>
                  <option value="right">House right · 2 ch free</option>
                </Select>
              )}
            </Field>
            <Field label="Idle output" error="The coordinator rejected an empty value at revision 47.">
              {(props) => <Input defaultValue="" {...props} />}
            </Field>
          </FieldGrid>
          <ChoiceRow>
            <Choice type="checkbox" defaultChecked={true} label="Emit LTC on this route" />
            <Choice type="radio" name="specimen-mode" defaultChecked={true} label="Show mode" />
            <Choice type="radio" name="specimen-mode" label="Program mode" />
          </ChoiceRow>
        </SpecSection>

        <SpecSection number="06 · State blocks" id="specimen-states" title="Absence should not look like a card containing data" detail="Two treatments, one job each. The ruled strip is the default and sits where the content would have been. The blanking plate is for a whole region that cannot render.">
          <div className="sm-strips__title">
            <span className="sm-spec-mark">A</span>
            <span className="sm-subhead">Ruled strip, the default</span>
            <span className="sm-small sm-muted">In-place, in-row. No fill, no radius, no card.</span>
          </div>
          {STRIPS.map((strip) => (
            <RuledStrip key={strip.label} absence={strip.absence} label={strip.label} fact={strip.fact} detail={strip.detail} />
          ))}

          <div className="sm-strips__title sm-spec-plate-title">
            <span className="sm-spec-mark">B</span>
            <span className="sm-subhead">Blanking plate, whole region</span>
            <span className="sm-small sm-muted">Hatch runs in the gutter only. Copy sits on clean surface.</span>
          </div>
          <div className="sm-spec-plates">
            <BlankingPlate
              absence="unobserved"
              stamp="No sig"
              eyebrow="Audio routing · unobserved"
              title="This node has never reported an audio inventory"
              detail="Output groups, free channels, and route ownership all come from the agent. Until it answers, there is nothing here to choose between."
              actions={<><Button>Run discovery</Button><Button variant="quiet">Enter a route manually</Button></>}
            />
            <BlankingPlate
              absence="noPermission"
              stamp="Perm"
              eyebrow="Access · insufficient permission"
              title={<>Your token is missing <span className="sm-data sm-spec-plate-scope">config:write</span></>}
              detail="The coordinator answered normally and refused. Principals and credentials stay hidden until a token with this scope is used."
              actions={<Button>Use a different token</Button>}
            />
          </div>
          <Callout>
            The dashed stamp and dashed underline mark absent evidence. They never borrow the shape of a settled
            state; generic unknown statuses retain a solid edge.
          </Callout>

          <div className="sm-strips__title sm-spec-plate-title">
            <span className="sm-spec-mark">C</span>
            <span className="sm-subhead">Not wired, a control with no endpoint</span>
            <span className="sm-small sm-muted">Drawn to its final shape, inert, and loud about it.</span>
          </div>
          <NotWiredBanner
            what="Firing an announcement"
            missing={<code className="sm-data">POST /cues/{'{id}'}/fire</code>}
            detail="This is not an absence of evidence. The screen is complete and the coordinator is not."
          />
          <ButtonRow>
            <NotWired>
              <Button variant="primary">Fire</Button>
            </NotWired>
            <NotWired>
              <Button>Fire and hold the bed</Button>
            </NotWired>
          </ButtonRow>
          <Callout>
            Amber, not red: nothing is broken and nothing has failed. The tag rides on the control so the warning
            cannot be scrolled away from the button it describes, and the child is forced disabled so the tag can
            never appear on something that works.
          </Callout>

          <div className="sm-strips__title sm-spec-plate-title">
            <span className="sm-spec-mark">D</span>
            <span className="sm-subhead">Notice, a refusal in two lines</span>
            <span className="sm-small sm-muted">Headline plus explanation, bordered so it reads as different from Callout.</span>
          </div>
          <Notice
            tone="bad"
            headline="This page and the coordinator disagree about which host you are on, so the sign-in was refused as a cross-site request."
            explanation="Usually a proxy in front of ShowMesh rewriting the Host header. Check that, or use a token instead."
          />
          <Notice
            tone="warn"
            headline={<>Too many attempts from this network right now. Wait <span className="sm-data">30s</span> and try again.</>}
            explanation="This is a rate limit on the network you are on, not a lockout on your account. Nothing is disabled."
          />
        </SpecSection>

        <SpecSection number="07 · Shell chrome" id="specimen-shell" title="Rail owns intent. The top bar owns the installation." detail="Mode, session, connection and principal belong in one persistent context bar, not stacked in a rail footer where nothing reads them.">
          <div className="sm-spec-shell">
            <ChromeBar
              showPicker={<button type="button" className="sm-showpicker"><span className="sm-meta sm-faint">Show</span>Winter Ridge 2026<span aria-hidden="true" className="sm-faint">▾</span></button>}
              mode={<span className="sm-mode-badge">Show mode</span>}
              nowPlaying={
                <>
                  <span className="sm-meta sm-faint">Now</span>
                  <span className="sm-truncate">Carol of the Bells</span>
                  <span className="sm-data sm-muted">1:42 / 2:48</span>
                  <span className="sm-small sm-faint sm-truncate">cycle 3 · next in 1:06</span>
                </>
              }
              connection={<ConnectionPill state="degraded" label="Reconnecting · 8 s" />}
              principal={<span className="sm-small sm-muted">erbartos</span>}
            />
            <ChromeProgress value={0.61} label="Position in the current item" />
            <ClockSkewStrip>This browser&rsquo;s clock is behind the coordinator&rsquo;s, the reference clock, by about 4 m 12 s. Every age and relative time shown here is off by roughly that much.</ClockSkewStrip>
            <div className="sm-spec-shell__body">
              <nav className="sm-spec-rail" aria-label="Rail specimen">
                <p className="sm-rail__group">Operate</p>
                <span className="sm-rail__link sm-spec-rail__link" aria-current="page">Show Night<span className="sm-rail__badge sm-rail__badge--live">LIVE</span></span>
                <span className="sm-rail__link sm-spec-rail__link">Dashboard</span>
                <span className="sm-rail__link sm-spec-rail__link">Live Control<RailBadge tone="bad" count={1} /></span>
                <p className="sm-rail__group">Author</p>
                <span className="sm-rail__link sm-spec-rail__link">Shows</span>
                <span className="sm-rail__link sm-spec-rail__link">Assets</span>
                <p className="sm-rail__group">System</p>
                <span className="sm-rail__link sm-spec-rail__link">Monitor<RailBadge tone="warn" count={3} /></span>
                <span className="sm-rail__link sm-spec-rail__link">Settings</span>
              </nav>
              <div className="sm-spec-shell__pane">
                <p className="sm-meta sm-faint">Rail states</p>
                <div className="sm-spec-rail-states">
                  {RAIL_STATES.map((state) => (
                    <div key={state.name} className={state.cls}>
                      {state.name}
                      <span className="sm-small sm-faint">{state.note}</span>
                    </div>
                  ))}
                </div>
                <p className="sm-small sm-muted sm-spec-rail-note">
                  Seven destinations, not twenty-five. Counts on the rail are attention counts, never inventory
                  counts: a badge means an operator has something to do.
                </p>
              </div>
            </div>
          </div>
        </SpecSection>

        <SpecSection number="08 · Evidence table" id="specimen-table" title="Dense rows, tabular numbers, local overflow" detail="Wide tables scroll inside their own container; the page never gains horizontal scrolling. Freshness rides in the row, not in a banner above it.">
          <TableWrap label="Signal evidence specimen">
            <Table>
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Signal</th>
                  <th>Value</th>
                  <th>Freshness</th>
                  <th className="sm-table__num">Observed</th>
                </tr>
              </thead>
              <tbody>
                {ROWS.map((row) => (
                  <tr key={row.node}>
                    <td>{row.node}</td>
                    <td className="sm-table__id">{row.signal}</td>
                    <td><StatusPair tone={row.tone} label={row.word} /></td>
                    <td><Freshness text={row.fresh} stale={row.stale} /></td>
                    <td className="sm-table__num">{row.at}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </TableWrap>
        </SpecSection>

        <SpecSection number="09 · Copy rules" id="specimen-copy" title="Shorter, and only where it changes a decision">
          <div className="sm-grid sm-grid--wide sm-spec-copy">
            <div className="sm-panel sm-spec-copy__card sm-spec-copy__card--bad">
              <p className="sm-meta sm-spec-copy__tag--bad">Today</p>
              <p className="sm-body sm-muted">“Authoritative current playback is unavailable. The coordinator could not be read. This does not prove that no external process is running.”</p>
              <p className="sm-small sm-faint">Three sentences to say one thing, and the caveat outweighs the fact.</p>
            </div>
            <div className="sm-panel sm-spec-copy__card sm-spec-copy__card--good">
              <p className="sm-meta sm-spec-copy__tag--good">Proposed</p>
              <p className="sm-body">Current run <span className="sm-spec-copy__unknown">unknown</span>, coordinator unreadable.</p>
              <p className="sm-small sm-muted">A show may still be running.</p>
              <p className="sm-small sm-faint">Fact first, at value weight. The caveat drops to helper size and stays.</p>
            </div>
          </div>
          <ul className="sm-spec-rules">
            <li>Label the outcome of a choice, not the field. <span className="sm-muted">“Program output group”, not “Audio route selection mode”.</span></li>
            <li>Helper text earns its line or it is deleted. <span className="sm-muted">If it repeats the label, it goes.</span></li>
            <li>Never explain the architecture in a form. <span className="sm-muted">ADR reasoning belongs in docs, not beside an input.</span></li>
            <li>State the reason literally, then the action. <span className="sm-muted">“Agent did not answer in 5 s. Run discovery again.”</span></li>
          </ul>
        </SpecSection>
      </div>
    </div>
  )
}
