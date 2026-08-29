import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  ApiError,
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  putFPPEndpointsConfig,
  getResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
  putResolumeInstancesConfig,
  getFPPMQTTConfig,
  getFPPMQTTConfigRevisions,
  putFPPMQTTConfig,
  getAssetsSettingsConfig,
  getAssetsSettingsConfigRevisions,
  putAssetsSettingsConfig,
  type AssetsSettingsConfigResponse,
  type ConfigAssetsSettingsPutPayload,
  type ConfigFPPEndpoint,
  type ConfigFPPMQTTPutRequest,
  type ConfigRevisionMeta,
  type FPPEndpointsConfigResponse,
  type FPPMQTTConfigResponse,
  type ResolumeInstancesConfigResponse,
} from '../api'
import { describeApiError } from '../app/session'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { useUnsavedChanges } from '../app/UnsavedChanges'

// Step 7 seam A (RES-008 D1): the configuration write surface's own view —
// the active `fpp.endpoints` configuration, its revision history, and an
// editor. Admin-only: `config:write` is the ONLY scope this surface is
// gated by, for reads and writes alike (there is no `config:read` scope —
// internal/coordinator/api/config.go's own doc comment records why), so
// this view treats a missing/stale/unavailable `config:write` grant as a
// reason not to even ATTEMPT the fetch, not merely a reason to disable the
// save button — the same "unknown/stale renders as not permitted, never
// permissive" rule (ADR-024 decision 12) applied to the whole page rather
// than one control, because every request this page could make (including
// both GETs) would be refused identically anyway.
//
// Track G seam G-2 (ADR-039) added a second section to this page, the
// resolume.instances editor below — this is what makes the "FPP &
// Resolume" nav label (ui/src/app/Layout.tsx) true: before this seam,
// nothing about connecting Resolume Arena existed on this page or anywhere
// else an operator could reach. Both sections share this one scope: Resolume
// instance configuration is exactly as admin-only-sensitive as the FPP
// endpoint list, for the identical reason (config.go's own top comment).
const CONFIG_WRITE_SCOPE = 'config:write'

// LoadState deliberately carries NO "not permitted" variant. An earlier
// version stored the not-permitted REASON string in this state, set by the
// fetch effect below, keyed only on `scopeGate.allowed`. That was a real
// bug, found only by testing a live transition in a real browser (CLAUDE.md's
// standing lesson: the suite renders one fixed session per test and never
// catches this): signed-out (`allowed: false`, reason "sign in") to signed
// in as a principal without config:write (`allowed: false`, reason "role
// does not include config:write") never changes `scopeGate.allowed`'s VALUE
// (false -> false), so the effect never re-ran and the page kept showing
// the stale "sign in" message to an operator who was, in fact, already
// signed in. The fix is structural: the not-permitted reason is read
// straight from `scopeGate` on every render, never captured into state.
type FPPLoadState =
  | { kind: 'loading' }
  // `reason` is the coordinator's own 404 detail, rendered verbatim rather
  // than replaced by a fixed sentence. Two different facts arrive as this
  // same 404: a coordinator nothing has ever configured, and a coordinator
  // whose startup migration of SHOWMESH_FPP_ENDPOINTS could not be
  // persisted, which IS collecting from that variable's endpoints while
  // the store holds no copy of them. This view used to assert the first
  // one unconditionally ("No fpp.endpoints configuration exists yet"),
  // which read as fine and was false in the second case while the
  // dashboard listed every host being polled. The server states the
  // reason; this page's job is to show it, not to author its own.
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: FPPEndpointsConfigResponse; revisions: ConfigRevisionMeta[] }

// ResolumeLoadState mirrors FPPLoadState's identical shape, for the
// resolume.instances section — see that type's own doc comment for the
// reasoning behind every branch, which applies unchanged here.
type ResolumeLoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ResolumeInstancesConfigResponse; revisions: ConfigRevisionMeta[] }

// FPPMQTTLoadState mirrors FPPLoadState/ResolumeLoadState's identical
// shape, for the fpp.mqtt section (Track G seam G-3).
type FPPMQTTLoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: FPPMQTTConfigResponse; revisions: ConfigRevisionMeta[] }

// AssetSettingsLoadState mirrors FPPLoadState/ResolumeLoadState's identical
// shape, for the assets.settings section — Track G seam G-4 (ADR-039).
type AssetSettingsLoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: AssetsSettingsConfigResponse; revisions: ConfigRevisionMeta[] }

// Configuration is the pre-tab-strip settings directory. The seven-tab
// Settings screen (SettingsPages.tsx, Settings.dc.html) now owns
// Connections/Content delivery/Render recovery/Access/Appearance/Audio
// defaults/Node routing/Mode, so this page keeps only what the tab strip
// does not cover: the three destinations the design guide calls
// relocations, not deletions (BUILDER-BRIEF.md) — show authoring (its own
// rail destination, Shows), the Show Night session definition editor, and
// FPP playlist definitions. Both of the latter two are BLOCKED awaiting
// an owner ruling on their new home (ROUTE-MAP.md), so their existing
// /config/* addresses stay the only way to reach them.
export function Configuration() {
  return (
    <div className="configuration-page">
      <h2 className="panel__title">Settings</h2>
      <p className="text-muted">
        Coordinator connections, content delivery, render recovery, access, appearance, audio, and
        mode moved to the tabbed <Link to="/settings/connections">Settings</Link> screen. The three
        destinations below have not: show authoring lives with the show it belongs to, and the other
        two are awaiting an owner ruling on where they land next.
      </p>
      <nav aria-label="Settings directory" className="config-index">
        <ul className="config-index__list">
          <li><Link to="/config/show">Show authoring</Link></li>
          <li><Link to="/config/night.session">Show Night session definitions</Link></li>
          <li><Link to="/config/fpp-playlist-definitions">FPP playlist definitions</Link></li>
        </ul>
      </nav>
    </div>
  )
}

// FPPEndpointsSection is this page's original content, unchanged in
// behavior — only extracted into its own component so a second,
// independent section (ResolumeInstancesSection, below) could be added
// alongside it without the two sharing load/save state that belongs to
// two entirely different configuration kinds.
export function FPPEndpointsSection() {
  const { clearUnsavedChanges } = useUnsavedChanges('connections-fpp-endpoints')
  const [state, setState] = useState<FPPLoadState>({ kind: 'loading' })
  const [rows, setRows] = useState<ConfigFPPEndpoint[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  // Step 7 seam A review defect 8: `saving` (React state) is what the
  // BUTTON renders from, but it is the wrong thing to GUARD on. Two fast
  // clicks each invoke handleSave from a render closure that captured
  // `saving` as it stood at THAT render — if both clicks land before
  // React has committed the first `setSaving(true)`, both closures see
  // `saving === false` and both proceed, creating two revisions from one
  // intended save. `savingRef` is synchronous and shared across every
  // closure immediately (no render in between required), so the second
  // call's guard check happens after the first call's guard-and-set,
  // regardless of React's render/commit timing.
  const savingRef = useRef(false)
  // A generation counter, not a boolean: "load again" needs to fire once
  // per Save success, and a plain `useEffect` dependency with no changing
  // value would never re-run after the first successful fetch.
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    let cancelled = false
    clearUnsavedChanges()
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([
          getFPPEndpointsConfig(),
          getFPPEndpointsConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setRows(config.payload.endpoints)
      } catch (err) {
        if (cancelled) return
        // A 404 is not a failure this page shows as an error: no
        // configuration is stored, which this view exists to let an admin
        // fix. Why none is stored is the coordinator's to say, and it says
        // it in the problem detail (`ApiError.message`) — see FPPLoadState's
        // `reason`. The detail is carried through rather than summarized
        // because one of the two cases it distinguishes is an active
        // warning not to remove SHOWMESH_FPP_ENDPOINTS.
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_configured', reason: err.message })
          setRows([])
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [clearUnsavedChanges, reloadGeneration])

  function addRow(): void {
    setRows((r) => [...r, { id: '', url: '' }])
  }

  function removeRow(index: number): void {
    setRows((r) => r.filter((_, i) => i !== index))
  }

  function updateRow(index: number, field: 'id' | 'url', value: string): void {
    setRows((r) => r.map((row, i) => (i === index ? { ...row, [field]: value } : row)))
  }

  async function handleSave(): Promise<void> {
    // Synchronous, shared-across-closures guard — see savingRef's own
    // comment for why `saving` (state) alone let two fast clicks both pass
    // this check and both PUT.
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      // Trimmed here, client-side, purely for operator convenience
      // (trailing whitespace pasted from elsewhere); the coordinator's own
      // validation (ADR-009: rejected before activation) is the actual
      // authority on what is acceptable, and this view renders whatever
      // it says via describeApiError below — it never second-guesses a
      // coordinator rejection with a different, client-invented message.
      // This also covers the 409 SHOWMESH_FPP_ENDPOINTS-still-set refusal
      // (Step 7 seam A review defect 3): the coordinator's own detail text
      // already states the variable, the two-step remedy, and why, so
      // there is nothing this view needs to special-case to render it as
      // an actionable message rather than a generic failure.
      const payload = { endpoints: rows.map((r) => ({ id: r.id.trim(), url: r.url.trim() })) }
      await putFPPEndpointsConfig(payload)
      clearUnsavedChanges()
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <section id="fpp-endpoints" className="panel config-section" data-unsaved-form="connections-fpp-endpoints">
      <h3 className="panel__title">FPP endpoints</h3>

      {state.kind === 'loading' && <p className="text-muted">Loading configuration…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {(state.kind === 'loaded' || state.kind === 'not_configured') && (
        <>
          {state.kind === 'not_configured' && (
            <p className="text-muted" role="status">
              {state.reason}
            </p>
          )}
          {state.kind === 'loaded' && (
            <div className="config-status" role="status">
              <p>
                Active revision {state.config.revision} (source {state.config.source}
                {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}).
              </p>
              <p>
                <strong>{state.config.restartRequiredReason}</strong>
              </p>
            </div>
          )}

          <div className="table-scroll">
          <table className="config-table">
            <thead>
              <tr>
                <th>Instance ID</th>
                <th>URL</th>
                <th aria-label="Remove" />
              </tr>
            </thead>
            <tbody>
              {rows.map((row, index) => (
                <tr key={index}>
                  <td>
                    <input
                      type="text"
                      aria-label={`Instance ${index + 1} id`}
                      value={row.id}
                      onChange={(e) => updateRow(index, 'id', e.target.value)}
                    />
                  </td>
                  <td>
                    <input
                      type="text"
                      aria-label={`Instance ${index + 1} url`}
                      placeholder="http://10.0.1.20"
                      value={row.url}
                      onChange={(e) => updateRow(index, 'url', e.target.value)}
                    />
                  </td>
                  <td>
                    <button
                      className="button-danger"
                      type="button"
                      onClick={() => removeRow(index)}
                      aria-label={`Remove instance ${index + 1}`}
                    >
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          <div className="config-actions">
          <button className="button--secondary" type="button" onClick={addRow}>
            Add instance
          </button>

          <div className="config-save-row">
            {saveError !== null && (
              <p role="alert" className="session-form__error">
                {saveError}
              </p>
            )}
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this configuration revision…"
            >
              {saving ? 'Saving…' : 'Save configuration'}
            </ScopedButton>
          </div>
          </div>

          <p className="text-muted">
            The list of FPP instances this coordinator polls, moved out of{' '}
            <code>SHOWMESH_FPP_ENDPOINTS</code> into this coordinator&rsquo;s own store.
          </p>

          {/* Revision history: a long, rarely-consulted list, kept apart
              from the status line above and the editor above that,
              a reasonable thing to start collapsed (nothing in it is
              stale/failed evidence rendered through EvidenceValue; it is
              a plain fetched list). */}
          {state.kind === 'loaded' && state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <RevisionsTable revisions={state.revisions} />
            </details>
          )}
        </>
      )}
    </section>
  )
}

// ResolumeInstancesSection is Track G seam G-2's own addition (ADR-039):
// the resolume.instances editor, alongside FPPEndpointsSection above.
// Unlike the FPP list, at most one instance is accepted (validation lives
// server-side — config.ValidateResolumeInstances — and this form mirrors
// that limit rather than enforcing its own copy of it, per ADR-030: "the
// browser may only mirror" server-side validation), so this renders one
// (id, url) pair, not a table of rows.
export function ResolumeInstancesSection() {
  const { clearUnsavedChanges } = useUnsavedChanges('connections-resolume-instances')
  const [state, setState] = useState<ResolumeLoadState>({ kind: 'loading' })
  const [id, setId] = useState('')
  const [url, setUrl] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    let cancelled = false
    clearUnsavedChanges()
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([
          getResolumeInstancesConfig(),
          getResolumeInstancesConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        const first = config.payload.instances[0]
        setId(first?.id ?? '')
        setUrl(first?.url ?? '')
      } catch (err) {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_configured', reason: err.message })
          setId('')
          setUrl('')
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [clearUnsavedChanges, reloadGeneration])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      // A blank id/url pair means "no instance configured" client-side —
      // an empty row is never sent as {"id":"","url":""}, which the
      // coordinator's own validation would reject anyway (ValidateNodeID
      // rejects an empty id) with a less useful message than simply
      // configuring zero instances.
      const trimmedID = id.trim()
      const trimmedURL = url.trim()
      const instances = trimmedID === '' && trimmedURL === '' ? [] : [{ id: trimmedID, url: trimmedURL }]
      await putResolumeInstancesConfig({ instances })
      clearUnsavedChanges()
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <section id="resolume-instances" className="panel config-section" data-unsaved-form="connections-resolume-instances">
      <h3 className="panel__title">Resolume</h3>

      {state.kind === 'loading' && <p className="text-muted">Loading configuration…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {(state.kind === 'loaded' || state.kind === 'not_configured') && (
        <>
          {state.kind === 'not_configured' && (
            <p className="text-muted" role="status">
              {state.reason}
            </p>
          )}
          {state.kind === 'loaded' && (
            <div className="config-status" role="status">
              <p>
                Active revision {state.config.revision} (source {state.config.source}
                {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}).
              </p>
              <p>
                <strong>{state.config.restartRequiredReason}</strong>
              </p>
            </div>
          )}

          <div className="table-scroll">
          <table className="config-table">
            <thead>
              <tr>
                <th>Instance ID</th>
                <th>URL</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>
                  <input
                    type="text"
                    aria-label="Resolume instance id"
                    value={id}
                    onChange={(e) => setId(e.target.value)}
                  />
                </td>
                <td>
                  <input
                    type="text"
                    aria-label="Resolume instance url"
                    placeholder="http://10.0.1.30:8080"
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                  />
                </td>
              </tr>
            </tbody>
          </table>
          </div>

          <div className="config-save-row">
            {saveError !== null && (
              <p role="alert" className="session-form__error">
                {saveError}
              </p>
            )}
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this configuration revision…"
            >
              {saving ? 'Saving…' : 'Save Resolume instance'}
            </ScopedButton>
          </div>

          <p className="text-muted">
            The Resolume Arena instance this coordinator connects to, moved out of{' '}
            <code>SHOWMESH_RESOLUME_URL</code>/<code>SHOWMESH_RESOLUME_ID</code> into this
            coordinator&rsquo;s own store. At most one instance is supported today.
          </p>

          {/* Revision history: same rationale as FPPEndpointsSection's own
              identical comment above. */}
          {state.kind === 'loaded' && state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <RevisionsTable revisions={state.revisions} />
            </details>
          )}
        </>
      )}
    </section>
  )
}

// HostRow is this section's client-side editing shape for one entry of
// ConfigFPPMQTTPayload.hosts — a map on the wire, a table of rows here,
// mirroring FPPEndpointsSection's identical map->rows translation.
type HostRow = { id: string; hostName: string }

// FPPMQTTSection is Track G seam G-3's own addition (ADR-039): the
// fpp.mqtt editor, alongside FPPEndpointsSection/ResolumeInstancesSection
// above. Unlike either of those, PUT here is a PARTIAL UPDATE (every field
// independently optional — ConfigFPPMQTTPutRequest's own doc comment), and
// this form's password field is the one place that distinction is load-
// bearing: GET never returns the password (ADR-039 decision 7), so the
// input always starts blank, and leaving it blank on Save must send NO
// "password" key at all — sending "" would explicitly clear a credential
// the operator never touched.
export function FPPMQTTSection() {
  const { clearUnsavedChanges } = useUnsavedChanges('connections-fpp-mqtt')
  const [state, setState] = useState<FPPMQTTLoadState>({ kind: 'loading' })
  const [brokerURL, setBrokerURL] = useState('')
  const [username, setUsername] = useState('')
  const [topicPrefix, setTopicPrefix] = useState('')
  const [hostRows, setHostRows] = useState<HostRow[]>([])
  // passwordInput is NEVER pre-filled from a server response — see this
  // function's own doc comment.
  const [passwordInput, setPasswordInput] = useState('')
  const [clearPassword, setClearPassword] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    let cancelled = false
    clearUnsavedChanges()
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([getFPPMQTTConfig(), getFPPMQTTConfigRevisions()])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setBrokerURL(config.payload.brokerURL)
        setUsername(config.payload.username)
        setTopicPrefix(config.payload.topicPrefix)
        setHostRows(Object.entries(config.payload.hosts).map(([id, hostName]) => ({ id, hostName })))
        setPasswordInput('')
        setClearPassword(false)
      } catch (err) {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_configured', reason: err.message })
          setBrokerURL('')
          setUsername('')
          setTopicPrefix('')
          setHostRows([])
          setPasswordInput('')
          setClearPassword(false)
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [clearUnsavedChanges, reloadGeneration])

  function addHostRow(): void {
    setHostRows((r) => [...r, { id: '', hostName: '' }])
  }

  function removeHostRow(index: number): void {
    setHostRows((r) => r.filter((_, i) => i !== index))
  }

  function updateHostRow(index: number, field: 'id' | 'hostName', value: string): void {
    setHostRows((r) => r.map((row, i) => (i === index ? { ...row, [field]: value } : row)))
  }

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      // hosts is a map on the wire, so a half-filled or duplicate-id row
      // CANNOT be represented in the payload — sending anyway would
      // silently drop or collapse the operator's input and the reload
      // would erase it. Blocked here with the row named instead; the
      // server still owns validation of what IS sent (ADR-030).
      const hosts: Record<string, string> = {}
      for (const [index, row] of hostRows.entries()) {
        const id = row.id.trim()
        const hostName = row.hostName.trim()
        // A fully blank row carries no operator input: skipped, not an error.
        if (id === '' && hostName === '') continue
        if (id === '' || hostName === '') {
          setSaveError(
            `Host ${index + 1} needs both an instance id and a HostName: fill in the ${
              id === '' ? 'instance id' : 'HostName'
            } or remove the row.`,
          )
          return
        }
        if (Object.hasOwn(hosts, id)) {
          setSaveError(`Host ${index + 1} repeats instance id "${id}". Instance ids must be unique.`)
          return
        }
        hosts[id] = hostName
      }
      // brokerURL/username/topicPrefix/hosts are always sent: this form
      // loads and displays every one of them (unlike the password), so
      // re-submitting what was already shown is ordinary full-form
      // editing, not the "never seen it" hazard the password has.
      const request: ConfigFPPMQTTPutRequest = {
        brokerURL: brokerURL.trim(),
        username: username.trim(),
        topicPrefix: topicPrefix.trim(),
        hosts,
      }
      if (clearPassword) {
        request.password = null
      } else if (passwordInput !== '') {
        request.password = passwordInput
      }
      // Otherwise "password" is omitted entirely — the currently stored
      // password (if any) is left exactly as it was (ADR-039 decision 5).
      await putFPPMQTTConfig(request)
      clearUnsavedChanges()
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <section id="fpp-mqtt" className="panel config-section" data-unsaved-form="connections-fpp-mqtt">
      <h3 className="panel__title">Event feed</h3>
      <p className="text-muted">How FPP&rsquo;s plugin reaches the coordinator. Playlist-entry identity arrives here.</p>

      {state.kind === 'loading' && <p className="text-muted">Loading configuration…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {(state.kind === 'loaded' || state.kind === 'not_configured') && (
        <>
          {state.kind === 'not_configured' && (
            <p className="text-muted" role="status">
              {state.reason}
            </p>
          )}
          {state.kind === 'loaded' && (
            <div className="config-status" role="status">
              <p>
                Active revision {state.config.revision} (source {state.config.source}
                {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}).
              </p>
              <p>
                <strong>{state.config.restartRequiredReason}</strong>
              </p>
            </div>
          )}

          <div className="form-field">
            <label htmlFor="fpp-mqtt-broker-url">Broker URL</label>
            <input
              id="fpp-mqtt-broker-url"
              type="text"
              placeholder="tcp://broker:1883"
              value={brokerURL}
              onChange={(e) => setBrokerURL(e.target.value)}
            />
          </div>
          <div className="form-field">
            <label htmlFor="fpp-mqtt-username">Username</label>
            <input
              id="fpp-mqtt-username"
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div className="form-field">
            <label htmlFor="fpp-mqtt-topic-prefix">Topic prefix</label>
            <input
              id="fpp-mqtt-topic-prefix"
              type="text"
              placeholder="falcon/player"
              value={topicPrefix}
              onChange={(e) => setTopicPrefix(e.target.value)}
            />
          </div>
          <div className="form-field">
            <label htmlFor="fpp-mqtt-password">
              Password {state.kind === 'loaded' && state.config.payload.passwordSet && '(currently set)'}
              {state.kind === 'loaded' && !state.config.payload.passwordSet && '(not set)'}
            </label>
            <p className="text-muted">
              The broker password is never shown once set: leave the password field blank to keep it
              unchanged.
            </p>
            <input
              id="fpp-mqtt-password"
              type="password"
              placeholder="leave blank to keep the current password"
              value={passwordInput}
              disabled={clearPassword}
              onChange={(e) => setPasswordInput(e.target.value)}
            />
            <label className="form-field--checkbox">
              <input
                type="checkbox"
                checked={clearPassword}
                onChange={(e) => {
                  setClearPassword(e.target.checked)
                  if (e.target.checked) setPasswordInput('')
                }}
              />{' '}
              Clear the stored password
            </label>
          </div>

          <div className="table-scroll">
          <table className="config-table">
            <thead>
              <tr>
                <th>Instance ID</th>
                <th>HostName</th>
                <th aria-label="Remove" />
              </tr>
            </thead>
            <tbody>
              {hostRows.map((row, index) => (
                <tr key={index}>
                  <td>
                    <input
                      type="text"
                      aria-label={`Host ${index + 1} instance id`}
                      value={row.id}
                      onChange={(e) => updateHostRow(index, 'id', e.target.value)}
                    />
                  </td>
                  <td>
                    <input
                      type="text"
                      aria-label={`Host ${index + 1} HostName`}
                      value={row.hostName}
                      onChange={(e) => updateHostRow(index, 'hostName', e.target.value)}
                    />
                  </td>
                  <td>
                    <button className="button-danger" type="button" onClick={() => removeHostRow(index)} aria-label={`Remove host ${index + 1}`}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
          <div className="config-actions">
          <button className="button--secondary" type="button" onClick={addHostRow}>
            Add host
          </button>

          <div className="config-save-row">
            {saveError !== null && (
              <p role="alert" className="session-form__error">
                {saveError}
              </p>
            )}
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this configuration revision…"
            >
              {saving ? 'Saving…' : 'Save FPP MQTT configuration'}
            </ScopedButton>
          </div>
          </div>

          <p className="text-muted">
            The Step 5 FPP MQTT collector&rsquo;s broker, credentials, topic prefix, and host map, moved
            out of <code>SHOWMESH_FPP_MQTT_*</code> into this coordinator&rsquo;s own store.
          </p>

          {/* Revision history: same rationale as FPPEndpointsSection's own
              identical comment above. */}
          {state.kind === 'loaded' && state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <RevisionsTable revisions={state.revisions} />
            </details>
          )}
        </>
      )}
    </section>
  )
}

// AssetsSettingsSection is Track G seam G-4's own addition (ADR-039): the
// assets.settings editor, alongside FPPEndpointsSection/
// ResolumeInstancesSection above. SHOWMESH_ASSET_DIR has no control here —
// it stays environment-only (ADR-039 decision 2). The PUT is a partial update
// (per-field optional, absent means keep-stored/default — ADR-039
// decision 5): a filled field is sent as typed, and a blank numeric
// field is omitted rather than coerced to Number('') === 0, which
// matters in the first-time zero-to-one setup path where every field
// starts blank (ADR-030: "the UI holds no authoring logic; validation
// is server-side").
export function AssetsSettingsSection() {
  const { clearUnsavedChanges } = useUnsavedChanges('content-delivery-assets')
  const [state, setState] = useState<AssetSettingsLoadState>({ kind: 'loading' })
  const [contentBaseURL, setContentBaseURL] = useState('')
  const [maxUploadBytes, setMaxUploadBytes] = useState('')
  const [syncIntervalSeconds, setSyncIntervalSeconds] = useState('')
  const [inventoryIntervalSeconds, setInventoryIntervalSeconds] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    let cancelled = false
    clearUnsavedChanges()
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([
          getAssetsSettingsConfig(),
          getAssetsSettingsConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setContentBaseURL(config.payload.contentBaseUrl)
        setMaxUploadBytes(String(config.payload.maxUploadBytes))
        setSyncIntervalSeconds(String(config.payload.syncIntervalSeconds))
        setInventoryIntervalSeconds(String(config.payload.inventoryIntervalSeconds))
      } catch (err) {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_configured', reason: err.message })
          setContentBaseURL('')
          setMaxUploadBytes('')
          setSyncIntervalSeconds('')
          setInventoryIntervalSeconds('')
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [clearUnsavedChanges, reloadGeneration])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      // A blank numeric field is OMITTED, never sent as Number('') === 0:
      // the PUT is a partial update (every field independently optional,
      // absent means keep-stored/default — ConfigAssetsSettingsPutPayload's
      // own doc comment), and an explicit 0 is refused. contentBaseUrl is
      // always sent: an empty string is its real "sync disabled" value.
      const maxUpload = maxUploadBytes.trim()
      const syncInterval = syncIntervalSeconds.trim()
      const inventoryInterval = inventoryIntervalSeconds.trim()
      const request: ConfigAssetsSettingsPutPayload = {
        contentBaseUrl: contentBaseURL.trim(),
        ...(maxUpload !== '' ? { maxUploadBytes: Number(maxUpload) } : {}),
        ...(syncInterval !== '' ? { syncIntervalSeconds: Number(syncInterval) } : {}),
        ...(inventoryInterval !== '' ? { inventoryIntervalSeconds: Number(inventoryInterval) } : {}),
      }
      await putAssetsSettingsConfig(request)
      clearUnsavedChanges()
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <section id="assets-settings" className="panel config-section" data-unsaved-form="content-delivery-assets">
      <h3 className="panel__title">Asset store settings</h3>

      {state.kind === 'loading' && <p className="text-muted">Loading configuration…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {(state.kind === 'loaded' || state.kind === 'not_configured') && (
        <>
          {state.kind === 'not_configured' && (
            <p className="text-muted" role="status">
              {state.reason}
            </p>
          )}
          {state.kind === 'loaded' && (
            <div className="config-status" role="status">
              <p>
                Active revision {state.config.revision} (source {state.config.source}
                {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}).
              </p>
              <p>
                <strong>{state.config.restartRequiredReason}</strong>
              </p>
            </div>
          )}

          <p className="text-muted">
            Content base URL must be an address every render node can reach, not this coordinator&rsquo;s
            own loopback or localhost address. A node fetching asset bytes from loopback fetches from
            itself, not from this coordinator.
          </p>

          <div className="table-scroll">
          <table className="config-table">
            <thead>
              <tr>
                <th>Content base URL</th>
                <th>Max upload bytes</th>
                <th>Sync interval (s)</th>
                <th>Inventory interval (s)</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>
                  <input
                    type="text"
                    aria-label="Asset content base URL"
                    placeholder="http://coordinator:8080 (empty disables sync)"
                    value={contentBaseURL}
                    onChange={(e) => setContentBaseURL(e.target.value)}
                  />
                </td>
                <td>
                  <input
                    type="number"
                    aria-label="Asset max upload bytes"
                    value={maxUploadBytes}
                    onChange={(e) => setMaxUploadBytes(e.target.value)}
                  />
                </td>
                <td>
                  <input
                    type="number"
                    aria-label="Asset sync interval seconds"
                    value={syncIntervalSeconds}
                    onChange={(e) => setSyncIntervalSeconds(e.target.value)}
                  />
                </td>
                <td>
                  <input
                    type="number"
                    aria-label="Asset inventory interval seconds"
                    value={inventoryIntervalSeconds}
                    onChange={(e) => setInventoryIntervalSeconds(e.target.value)}
                  />
                </td>
              </tr>
            </tbody>
          </table>
          </div>

          <div className="config-save-row">
            {saveError !== null && (
              <p role="alert" className="session-form__error">
                {saveError}
              </p>
            )}
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this configuration revision…"
            >
              {saving ? 'Saving…' : 'Save asset settings'}
            </ScopedButton>
          </div>

          <p className="text-muted">
            The asset store&rsquo;s operator-facing settings, moved out of{' '}
            <code>SHOWMESH_ASSET_CONTENT_BASE_URL</code>/<code>SHOWMESH_ASSET_MAX_UPLOAD_BYTES</code>/
            <code>SHOWMESH_ASSET_SYNC_INTERVAL</code>/<code>SHOWMESH_ASSET_INVENTORY_INTERVAL</code> into
            this coordinator&rsquo;s own store. <code>SHOWMESH_ASSET_DIR</code> is unaffected; it stays
            environment-only.
          </p>

          {/* Revision history: same rationale as FPPEndpointsSection's own
              identical comment above. */}
          {state.kind === 'loaded' && state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <RevisionsTable revisions={state.revisions} />
            </details>
          )}
        </>
      )}
    </section>
  )
}

// RevisionsTable renders a config kind's own revision history — shared by
// both sections above, since ConfigRevisionMeta/ConfigRevisionsResponse is
// already one shape common to every configuration kind (api/openapi.yaml's
// own ConfigRevisionsResponse description).
function RevisionsTable({ revisions }: { revisions: ConfigRevisionMeta[] }) {
  return (
    <div className="table-scroll">
    <table className="config-table">
      <thead>
        <tr>
          <th>Revision</th>
          <th>Active</th>
          <th>Created at</th>
          <th>Created by</th>
          <th>Source</th>
        </tr>
      </thead>
      <tbody>
        {revisions.map((rev) => (
          <tr key={rev.revision}>
            <td>{rev.revision}</td>
            <td>{rev.active ? 'active' : ''}</td>
            <td>{formatAbsolute(rev.createdAt)}</td>
            <td>{rev.createdByPrincipalName ?? '(coordinator startup migration)'}</td>
            <td>{rev.source}</td>
          </tr>
        ))}
      </tbody>
    </table>
    </div>
  )
}
