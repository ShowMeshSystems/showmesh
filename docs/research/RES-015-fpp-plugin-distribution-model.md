# RES-015: FPP Plugin Repository and Distribution Model

[Architecture](../architecture/ARCHITECTURE.md) · [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) · [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) · [Tracker](README.md)

Status: planned · Risk: **high** (raised from medium, see §1) · Verification: **L1, source-verified** against FPP `9.4`, `9.5.3`, and `master`; every acceptance criterion still needs the bench Pi

Researched 2026-08-12 by three parallel source-verification passes: plugin-manager and packaging mechanics, plugin callback hooks and FPP's command-invocation path, and a corpus survey of the other 56 plugins in FPP's official list. Nothing was executed and no FPP instance was touched. **L1 means read in the source, not observed running.**

## 1. Why this record's scope and risk both changed

This record was written as a packaging question: which repository layout FPP's plugin manager wants, and whether an installer may fetch a built `showmesh-agent` from elsewhere. Both of those are now answered (§4, §5).

The research also answered a question this record did not ask, and the answer moves RES-015 onto the critical path of the macro step. [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7 requires a `401` or `403` from the coordinator to be a defined fallback trigger on the FPP host. **FPP's native command mechanism cannot discharge that obligation, and the reason is structural rather than a missing feature** (§7). So there is no version of the macro step that does not ship ShowMesh-authored code onto an FPP host, which is exactly what this record governs. Risk is raised from medium to high on that basis.

The scope is therefore broadened from "how the plugin is packaged" to **"how ShowMesh code gets onto an FPP host, and what it can and cannot observe or do once it is there."** That expansion is recorded here rather than made silently.

## 2. Provenance

| Source | Pin |
|---|---|
| `FalconChristmas/fpp` master | `de6d0199839101a6d0caf11052ec5b5e28bc7b80` |
| `FalconChristmas/fpp` `9.5.3` | `4d347be2f89dc9ada265c731bc2bb01966463689` (what `bench/fpp-multisync/` pins) |
| `FalconChristmas/fpp` `9.4` | `02aebb9c8ebefdd56ef19e028e6b9a515408760c` (deployed fleet) |
| `FalconChristmas/fpp` `v10.0-beta` | `64875e8d202aba1224d28fd91b41e9fccaaf03a3` |
| `FalconChristmas/fpp-data` master | `6f64995194896daddea84e4b84edec5cdc2a57d2` |
| `FalconChristmas/fpp-plugin-Template` master | `5c0dc73545f5e032902135d8770b1599d489eabe` |
| `KulpLights/fpp-FPPMon` master | `6da873f5da19e64632f7e0deb2459a1df827acec` |
| `FalconChristmas/fpp-brightness` master | `f85cab1ec633d163b3736f06829a99852fa85da1` |

All web access 2026-08-12. The corpus survey in §6 covered 56 repositories, 3,976 blobs, and 145 install, uninstall, Makefile, and hook scripts at default-branch HEAD. **The corpus facts and several packaging facts were established by delegated sub-surveys rather than read personally by the reporting pass**, and are marked where that is so.

**Two version regimes, not three.** `scripts/install_plugin` and `scripts/uninstall_plugin` are byte-identical between FPP 8.4.2 and `9.4`, and `master` is byte-identical to `v10.0-beta`. So the design targets **FPP 9.x and earlier** and **FPP 10**. The fleet is entirely in the first regime and is expected to cross into the second.

**Independently re-verified during fold-in**, because it is the single most load-bearing finding in the record: `src/commands/MediaCommands.cpp` was fetched at all three refs and checked directly. `CURLINFO_RESPONSE_CODE` appears zero times on every ref. `CURLOPT_HTTPHEADER` appears zero times on every ref. On `9.4` and `9.5.3`, line 154 reads `virtual bool isError() override { return m_curl == nullptr || m_curlm == nullptr; }`. FPP's own master comment at line 122 states the consequence: "isError()/isDone() below only look at handle setup and CURLMSG_DONE, not the HTTP status or transfer result."

## 3. Questions, and where each is now answered

| Original question | Answer |
|---|---|
| What does the plugin manager require, and does a thin installer-only repo satisfy it? | §4. Yes, with a positive existence proof. |
| Can `install.sh` fetch a pinned binary from a separate repo? Offline installs? | §5. Yes. Offline through the Plugin Manager is impossible. |
| What target architectures, and does ADR-012's pipeline cover them? | §5.3. **No, it does not.** |
| How does the plugin pin a compatible version, and what happens on a protocol bump? | §5.4. FPP expresses FPP-version ranges only; anything about ShowMesh's own protocol is ours to solve. |
| What does uninstall need to clean up, and is the hook called reliably? | §5.5. Defined, mandatory for us, and **not reliably called**. |
| Which callback hooks exist, and are they sufficient without a second UDP 32320 listener? | §7.1. Sufficient for playlist and media lifecycle; **not** for sequence lifecycle on a standalone player. |
| Does the registry forbid binary blobs or impose review constraints? | §6. The feared prohibition does not exist. Other constraints do. |

## 4. Repository structure and `pluginInfo.json`

**Fact.** FPP discovers an installed plugin solely by `<pluginDirectory>/<name>/pluginInfo.json` existing. Nothing else on disk is required.

**Fact.** There is now a strictly enforced JSON Schema in `fpp-plugin-Template` with `"additionalProperties": false`, validated in CI on every submission. Required top-level fields are exactly `repoName`, `name`, `author`, `description`, `homeURL`, `srcURL`, `bugURL`, `versions`.

**Fact.** Constraints that bind ShowMesh directly:

- `srcURL` must match `^https://(www\.)?github\.com/`. **GitHub is the only permitted clone source**, described in the schema as "the trust anchor for the Official badge."
- `repoName` is the on-disk directory name and is run through `escapeshellcmd`. A mismatch against the repo name parsed from `srcURL` raises `reponame-mismatch`.
- Each `versions[]` entry requires `minFPPVersion`, `maxFPPVersion`, `branch`, and `sha`, with no additional properties.
- `infoURL` and `useCredentials` must not be authored; FPP injects them.
- `dependencies`, `minMemoryMB`, and `minCpuCores` are **FPP 10 only**, and FPP 9 and earlier silently ignore the whole `dependencies` block with no error.

**Fact.** The file must be strict JSON. A `//` or `/* */` comment anywhere makes the plugin fail both to load and to install.

**Fact, and this is the answer to the record's opening question.** A thin installer-only repo satisfies FPP. Nothing requires source, a `Makefile`, or a committed artifact. The existence proof is `KulpLights/fpp-FPPMon`, authored by an FPP core developer and officially listed, whose repository root holds no source and no Makefile. FPP's own linter names this shape in a source comment as recognized practice: "fetched prebuilt from a GitHub release (e.g. fpp-FPPMon: no Makefile at all …)".

## 5. Install, upgrade, uninstall, and the architecture matrix

### 5.1 Install mechanics, and three silent-failure modes

**Fact.** On FPP 9.x and earlier the flow is: `git clone --single-branch --branch <branch> <srcURL> <name>` into the plugin directory, `git reset --hard <sha>` if one is declared, `chown -R fpp:fpp`, then run `scripts/fpp_install.sh` if it exists and is executable, else the repo-root variant. Scripts run **as root**.

**Fact.** Three ways an installer fails silently on the deployed fleet, each of which this record exists to prevent us rediscovering:

- **The working directory is the plugin *parent*, not the plugin directory.** `install_plugin` does `cd ${PLUGINDIR}` and then invokes the script by absolute path. Real plugins compensate explicitly.
- **`FPPDIR` and `SRCDIR` arrive as positional arguments only, not environment variables**, because the Plugin Manager exports bare `sudo` rather than `sudo -E`, stripping the exported values. The PHP upgrade path uses the environment form instead, so the two paths disagree. FPP's own master source records this as a long-unnoticed bug whose output "was never logged", which is this project's own "a test that passes whether or not the bug is present" lesson appearing in someone else's codebase. **Any installer reading `$FPPDIR` without a `${FPPDIR:-/opt/fpp}` default fails on the entire current fleet.**
- **A script committed at mode 0644 is skipped silently.** Both install and uninstall gate on `[ -x ]`. FPP 10 added mode normalization and explains why; 9.4 has none. The registry linter treats this as a BLOCKER, noting for the uninstall case that "uninstall 'succeeds' while every side effect is left behind on the host with no error."

**Fact.** FPP 10 restructures install into two phases with a dependency-resolution callback, and sets `core.fileMode false` per clone so FPP's own `chmod` does not dirty the tree and break later pulls.

### 5.2 Fetching a binary from another repository

**Fact.** Nothing in `install_plugin` constrains what the install script does. FPP's guidelines permit `apt-get`, `npm`, and `pip`, and say anything else the install genuinely needs belongs in the install script. The only prohibition is piped remote execution (`curl … | bash`), which is a BLOCKER.

**Fact.** Offline and air-gapped install through the Plugin Manager is **impossible**. The flow is always a `git clone` from a `github.com` URL, and the plugin list itself is fetched live from `raw.githubusercontent.com`. Master's UI degrades cosmetically when offline (hiding star counts, skipping update checks) and offers no local-file, archive-upload, or offline path. **RES-015's test-matrix line "install with no network reachable to the release host" must be split**: the release host being unreachable is testable and must fail cleanly; fully air-gapped install is out of scope for this path and needs a documented manual procedure.

### 5.3 The architecture matrix, and the largest concrete gap in the plan

**Fact.** The deployed fleet spans both ARM word sizes: `fpp-player` is a Raspberry Pi 3 Model B+ on FPP 9.4, `fpp-remote-a` is a BeagleBone Black, and `fpp-remote-b` is a PocketBeagle2. FPP's installer sets BeagleBone Black to armhf and BeagleBone 64 to aarch64 by a `uname -m` test.

**Fact. ShowMesh's build pipeline does not cover this fleet.** CI builds `linux/amd64` and `linux/arm64` only, and produces container images rather than standalone agent release artifacts. A BeagleBone Black needs `GOARCH=arm GOARM=7`, which is not built today. **This directly answers the record's own sub-question: ADR-012's pipeline does not cover the deployed fleet without change.**

**Fact. `uname -m` must not be used to select an artifact.** A Pi 4 or 5 boots a 64-bit kernel even under a 32-bit FPP, so `uname -m` reports `aarch64` for what is really an armhf userspace. `/etc/fpp/arch`, the clean answer, is **FPP 10 only**; FPP added it precisely because `FPPPLATFORM` alone cannot distinguish 32-bit from 64-bit Pi, since both write "Raspberry Pi".

**This is the most useful single result in the record, and it is useful because of how it was established.** Two plugins, written independently by different authors, defend against the same trap by *different mechanisms*: `fpp-FPPMon` reads the ELF class of an actual FPP binary (byte 5 of the ELF header), and `BackgroundMusicFPP-Plugin` probes for the 64-bit dynamic linker at `/lib/ld-linux-aarch64.so.1`. Two unrelated implementations converging on the same hazard removes any doubt that it is real on a Pi fleet rather than one author being over-cautious. It is also this project's own "duplication found the bug" pattern showing up in someone else's ecosystem. **The bench probe should record both detection methods and confirm they agree.**

### 5.4 Version pinning and upgrade

**Fact.** `versions[]` gates on FPP version and platform only. FPP walks the list and picks a matching entry; `sha: ""` tracks branch tip and a real SHA freezes it.

**Fact.** An open-ended `maxFPPVersion` silently becomes major-scoped: FPP rewrites it to `(major - 1).999`, marks the entry untested, hides it below a UI level, and warns "Install untested plugin at your own risk." So a single open-ended entry does not error when a new FPP major appears, it quietly disappears from view. Adding a `versions[]` entry per FPP major looks like unavoidable ongoing maintenance.

**Fact.** "Behind" is computed by git, comparing the checked-out branch against its origin. It never re-reads `pluginInfo.json` to see whether a different entry now applies.

**Fact.** FPP 9.x upgrade is `git pull` with a `git clean -fd && git pull` fallback, and it honors `scripts/fpp_install.sh` but **not** `fpp_upgrade.sh`. FPP 10 moved upgrade into its own script, resolves the branch first, hard-resets to origin on failure (noting that `git clean -fd` alone "did NOT help because it only removes UNTRACKED files, not modified tracked ones"), honors `fpp_upgrade.sh` first, and refuses to report success if the repository did not actually advance, fixing a bug where a failed update followed by a succeeding install script masqueraded as a successful upgrade.

**Fact.** There is **no mechanism to declare a version range against anything other than FPP itself**. `dependencies.plugins` names other plugins with no version constraint. Nothing expresses "requires ShowMesh coordinator protocol at least N". That is entirely ours to solve in our own payloads, and it is a real obligation: the record's own acceptance criterion requires a protocol-incompatible pairing to fail loudly.

### 5.5 Uninstall

**Fact.** The hook is defined and called before deletion, and it is **not reliably called**. Three independent gaps: the `[ -x ]` mode gate, the hook's exit code being discarded because the script's status is that of its final `rm -rf`, and FPP deleting the directory unconditionally regardless of outcome.

**Fact.** Everything an install creates outside the plugin directory must be reversed: systemd units disabled then removed, timers, cron entries, symlinks, and files under `/etc` and `/usr/local`. Uninstall must be idempotent and exit 0 on a second run.

**Fact.** Two linter BLOCKERs enforce this, keyed on the plugin touching systemd or cron. **A ShowMesh plugin installing a supervised agent service trips `no-uninstall` unless it ships the removal script**, so for us the uninstall script is mandatory rather than optional, even though 21 of 56 listed plugins ship none (delegated survey).

## 6. The registry, and what the corpus actually does

**Fact.** Listing is gated and reviewed: submit via a GitHub issue form, an automated check runs, and a maintainer still reviews by hand on category fit. Inclusion is explicitly discretionary and not guaranteed, with a stated concern about AI-generated plugin submissions.

**Fact.** Severity tiers matter more than they look. Blocker must be fixed, and **Best practice "also must be fixed for a first-time submission (already-listed plugins get more leeway here; yours doesn't yet)."** We do not get the leeway.

**Fact. There is no prohibition on committing binary blobs.** The concern this record was written around does not exist.

**Fact.** There *is* a rule on install-time downloads and it is satisfiable. `unverified-package-install` fires on a `chmod +x` of a downloaded file with no checksum or signature check, and is suppressed file-wide by the presence of `sha256sum`, `gpg --verify`, or similar. It is Best practice severity, so it would block a first listing, and a sha256 verify clears it.

**Fact.** Licensing imposes nothing on us. A LICENSE file is Optional severity and no specific license is mandated. Apache-2.0 per ADR-010 is fine.

**Fact.** Other BLOCKERs we must design around: no `curl | bash`; no `systemctl restart fppd`, use `setSetting restartFlag 1`; never call fppd's HTTP port directly, use `http://localhost/api/...`; no binding all interfaces; no running the install script from a lifecycle hook; no unbounded `git` network call in a lifecycle hook.

**Fact, and the one listing risk that cannot be engineered away.** `phone-home` is a BLOCKER banning off-box telemetry "except where that data transmission is essential to the plugin's actual function." An agent reporting to an operator-owned coordinator is architecturally that exception, but the determination is made by a human maintainer, not by the checker. It deserves to be stated in the submission rather than discovered in review.

### 6.1 What 56 other plugins do, and what it changes

All four facts below are from the delegated corpus survey.

- **Committing a compiled binary has zero precedent.** A grep for every binary artifact extension across all 3,976 blobs in 56 repositories returned no matches.
- **Fetching from a separate repository or vendor has three precedents**, of which only one is a copyable pattern; the other two hand off to a vendor's own installer.
- **Building on device dominates: 16 of 56.**
- **Zero of 145 scripts verifies a checksum or signature.** The one copyable strategy-(c) precedent resolves `releases/latest` at install time with no pin, so two hosts installed a week apart get different binaries.

**This inverts one of this record's own decisions.** The recorded fallback, "vendor a prebuilt binary directly in the plugin repo", is the single strategy with **zero precedent in the listed set**, while the working assumption has three. Retreating to the fallback would be retreating *away* from established practice, for a risk that does not exist. It is struck in §9 rather than left standing unused.

**And it strengthens the working assumption's justification.** The corpus builds from source because FPP's entire plugin toolchain exists to compile C++ against `/opt/fpp/src`. **ShowMesh cannot use that path at all**: the agent is a Go static binary and FPP hosts carry no Go toolchain. So we are outside the dominant model by construction, and fetching a prebuilt artifact is not a preference, it is the only option open to us. That is a far stronger argument than "it seems fine," and it belongs in the record.

**Two build constraints the corpus supplies, each with a cited failure:**

- **Do not ship a `Makefile`.** FPP's `scripts/functions` runs `make -C` over every plugin directory containing one, unconditionally, on core upgrade. A plugin that fetches a Go binary *and* carries a Makefile has two competing producers of one artifact.
- **Ship a cheaply guarded `preStart.sh` repair check**, of the form `if [ ! -f <artifact> ]; then refetch; fi`. The documented failure it defends against is an SD image cloned from a machine of a different architecture, which is live on any fleet built from Pi images. It must be a no-op in the common case: an unconditional refetch trips two BLOCKERs and delays every `fppd` start, which the reference plugin's own comment records having done.

## 7. What ShowMesh code can observe and do on an FPP host

This section is outside the record's original scope and is the reason for §1.

### 7.1 Callback hooks, and a correction to ADR-013's mechanism

**Fact.** There are two plugin kinds with different hook surfaces: a **script plugin** (invoked by FPP commands, wired through `commands/descriptions.json`) and a **C++ plugin** (a `.so` loaded into `fppd`).

**Fact.** `playlistCallback` fires with exactly four action strings, verified across every call site on both master and 9.5.3: `start`, `playing`, `stop`, `query_next`. **There is no per-item-change action**; item advancement is observable only as a repeated `playing` carrying a new section and item index.

**Fact.** `eventCallback` is **dead code**. It is declared in `Plugin.h` and called by nothing anywhere in the tree, on master or 9.5.3. A plugin overriding it receives nothing, forever, with no error. This is a trap for whoever writes our plugin and is recorded so they do not find it the hard way.

**Fact, and a correction to how ADR-013 is read.** ADR-013 directs FPP-side observation through "FPP's plugin callback boundary, not a second socket", and names `addMultiSyncPlugin`. That specific mechanism is **gated twice**: every `SendSeq*` call is wrapped in `if (multiSync->isMultiSyncEnabled())`, and each sender returns early when the control socket is closed, before the plugin fan-out. **So a plugin observing sequence lifecycle through `MultiSyncPlugin` sees nothing on a host that is not a configured MultiSync master.** ADR-013's conclusion is unaffected and its constraint stands. The mechanism it names is the gated one; `playlistCallback` is the reliable one.

**Fact.** Frame-level hooks (`modifySequenceData`, `modifyChannelData`) run **inline on the channel output thread, per frame**. Blocking there stalls output directly. They are unusable as an observation channel without a lock-free hand-off, and using them at all puts ShowMesh nearer the timing path than the standing constraints want.

**Direct answer to the ADR-013 question.** The hooks are sufficient for playlist and media lifecycle without a second socket, and insufficient for sequence-level lifecycle on a standalone player. Sequence *identity* is reachable, because the `playing` callbacks carry playlist JSON including sequence names; sequence *lifecycle timing* is not.

### 7.2 FPP cannot detect a failed outbound call, and this corrects an ADR-024 argument

**Fact, re-verified personally during fold-in.** Three native outbound command types exist: `URL`, `MQTT`, and `Run Script`. The `URL` command takes exactly three arguments, url, method, and post data, and **`CURLOPT_HTTPHEADER` is set zero times on every ref**. There is no way to send an `Authorization` header.

**Fact.** The HTTP status code is never read. `CURLINFO_RESPONSE_CODE` appears zero times in the file on every ref. A `401` is `CURLE_OK` and is indistinguishable from a `200` by any code in FPP.

**Fact.** On `9.4` and `9.5.3`, which is the deployed fleet, `isError()` tests handle setup only. **A URL command that hits DNS failure, a timeout, or connection refused reports success.** Master added a transfer-failure flag which catches transport failure but still not HTTP status.

**Fact.** On the scheduler and preset paths the returned `Result` is discarded outright, so there is nowhere to put a check even if the status were readable. A failed command is also invisible to a playlist: an unresolvable command marks the item finished exactly as a success, which FPP's own source comment says lets "a show silently skip this step forever with nothing but a log line to show for it."

**This corrects an argument in ADR-024, and the correction has the same shape as the error that record was written to fix.** Decision 7 reasons from an asymmetry: a coordinator outage is a transport failure, *which is what an ADR-004 fallback detects and fires on*, whereas a `403` is a successful conversation that fires nothing, making the refusal case "strictly worse than the genuine outage."

Through FPP's native `URL` command, **neither case fires anything, because FPP can detect neither.** The record assumed a detection capability that does not exist. Its conclusion survives untouched, `401` and `403` must be defined fallback triggers, but the asymmetry it argued from is not real at the mechanism level, and the true consequence is larger than the record states: **all of it, including transport-failure detection, must live in ShowMesh-authored code on the FPP host.**

The generalizable shape is worth carrying, because it is a third variant of a defect this project keeps meeting. ADR-024 corrected an argument made against the wrong failure *direction*. This is an argument made against a failure-detection *capability that was assumed rather than checked*. In both cases the conclusion happened to survive and the reasoning did not.

**An ADR-024 amendment recording this is pending the owner's decision** and is deliberately not written here, because it revises the reasoning of an accepted record rather than noting an implementation gap.

**A lighter integration than a C++ plugin exists.** A script plugin registering a command through `commands/descriptions.json` gets an ordinary forked process, which can make an authenticated request, read the status code, branch, and execute the local fallback itself. It also sidesteps the C++ ABI split entirely, which is a real problem: master gates on a plugin API version and refuses a mismatch, while 9.5.3 has no version check at all and will silently load a binary master would reject. Two hazards attach to the script path and are recorded so they are not rediscovered: the command's `execve` environment contains only `MEDIADIR`, `FPPDIR`, and `SCRIPTDIR` with **no `PATH`**, so tools must be resolved absolutely; and the exit status is discarded, so the script must own its own failure handling.

### 7.3 There is no idempotency carrier, and no retry

**Fact.** Nothing in FPP's command path carries an invocation identity: not the command interface, not its result type, not the cross-host wire packet. The nearest thing is a schedule row index, which is identical on every firing of that row and therefore cannot deduplicate.

**Fact.** There is no retry. The cross-host path is a single non-blocking `sendto` with no error check, and reports success as soon as the packet is queued.

**Consequence.** ARCHITECTURE §8.1 requires an idempotency key on every command, and **it must be minted by ShowMesh's own code per invocation**, because FPP provides nothing to derive it from. The absence of retry is the good news attached: FPP will not duplicate a command on its own, so the duplication ShowMesh must defend against comes from the operator, from overlapping schedule entries, and from the MultiSync command fan-out.

### 7.4 A credential on an FPP host is effectively public

ADR-024 is written around a `scheduler` machine token living on the FPP host. On FPP's defaults that token is exposed by at least four independent paths.

**Fact.** **Every command execution publishes to MQTT `command/run` with its arguments in cleartext**, from every trigger source, documented and present on all refs. A token passed in a URL query string, which is the only way to attach one to a native `URL` command at all, is therefore broadcast on every invocation. Our fleet publishes to the operator's live home-automation broker.

**Fact.** FPP writes its config files mode `0664`, world readable, owned by `fpp`.

**Fact.** `GET /api/configfile/**` streams the raw contents of any file under the config directory with no allowlist, and per-setting and per-plugin-setting endpoints return live values with no masking even for password-typed settings.

**Fact.** The web UI and API are **unauthenticated by default**; the controlling setting defaults to no password.

**Fact.** Backup redaction is an **exact key-name match list**, and on `9.4` and `9.5.3` that list is only `emailpass`, `password`, `secret`. There is no generic token, apikey, or bearer pattern on any ref, so a key named anything ShowMesh-shaped is not redacted, and backups download as plaintext JSON. FPP's own precedent confirms the pattern rather than contradicting it: a GitHub personal access token setting is documented as stored plaintext and is not in the redaction list on any ref.

**Consequence.** Any ShowMesh credential placed on an FPP host must be treated as readable by anyone who can reach that host's web UI.

**Owner decisions, 2026-08-12, which turn this from a finding into a constraint.**

- **Cleartext on the show LAN is accepted for commands, macros, status, and telemetry, and is not accepted for a credential.** The reasoning is recorded in `SECURITY.md`: this is a holiday light display on an isolated network, and a leaked secret is the one failure the VLAN does not contain, because it outlives the packet that carried it and can be replayed by anyone who captured it once.
- **Therefore FPP's native `URL` command is unusable for an authenticated call**, and not merely inferior to a plugin. It can set no header on any version, so the only place a credential can go is the URL, and §7.4's first fact then publishes it in the clear on every invocation. Combined with §7.2, which shows the native command cannot detect a failure either, **the ShowMesh-authored plugin is required on two independent grounds**. The script-plugin form in §7.2 remains the lighter option.
- **Improving FPP's own posture is out of scope.** It is upstream work and is not worth taking on now. The exposures are documented here and in `SECURITY.md`'s out-of-scope list, and ShowMesh designs around them rather than filing them as its own defects. What remains ShowMesh's defect, and is worth reporting as one, is ShowMesh putting a secret somewhere FPP will expose it.
- The `scheduler` principal's scope bundle stays as narrow as ADR-024 decision 4 permits, with a credential cheap to rotate, because the host it lives on cannot keep it secret.

## 8. Acceptance criteria

Unchanged in substance, with two amendments from §5.2 and §5.4:

- A plugin repository installs cleanly on a real FPP instance via FPP's own plugin manager UI, not a hand-run script.
- The installed agent binary matches the pinned release checksum and runs as a supervised service that survives an FPP reboot.
- Uninstall removes the service and binary with no leftover process or stale MQTT Last Will registration.
- A protocol-incompatible agent and coordinator pairing fails loudly rather than silently misbehaving. **Nothing in FPP expresses this**, so it is entirely ShowMesh's mechanism to build (§5.4).
- No second listener on UDP 32320 is introduced; FPP-side observation goes through a documented callback hook. **Use `playlistCallback`, not `MultiSyncPlugin`** (§7.1).
- **Split from the old "install with no network":** the release host being unreachable must fail cleanly and is testable; fully air-gapped install is out of scope for the Plugin Manager and needs a documented manual procedure (§5.2).

**None of these is met.** All require the bench Pi.

## 9. Decision, fallback, and revalidation

**The working assumption stands and is now well supported.** A separate thin plugin repository whose install script fetches a pinned `showmesh-agent` release is recognized practice, has a listed core-developer plugin as a template, and is named as recognized in FPP's own linter source. It is also, for ShowMesh specifically, the **only** available strategy, because the dominant build-on-device model requires a Go toolchain that FPP hosts do not have.

**The recorded fallback is struck.** "Vendor a prebuilt binary directly in the plugin repo per tagged release" was reserved against a registry prohibition that does not exist, and it is the one strategy with zero precedent across 56 listed plugins. It is removed rather than left standing, because a fallback nobody should take is a trap for a future reader.

**Amendments to the plan as previously written:**

- The script is `scripts/fpp_install.sh`, and it must be **committed with the executable bit set**.
- "Pinned" must mean **checksum-verified**, not merely a pinned URL. This clears a Best practice finding that would block a first listing, and it puts ShowMesh ahead of every strategy-(c) plugin in the set, since none of the 145 corpus scripts verifies anything.
- Build artifacts for **armhf** as well as arm64 and amd64, and produce standalone agent releases rather than only container images.
- **Never select an artifact with `uname -m`.**
- **Ship no `Makefile`**, and **ship a cheaply guarded `preStart.sh` repair check**.
- Ship `scripts/fpp_upgrade.sh`, honored on FPP 10 and ignored on 9.4, so it is additive and safe.
- Carry `versions[]` entries for both the 9.x and FPP 10 regimes.

**Open bench items**, all requiring a real FPP instance:

1. Read `/etc/fpp/platform` and the ELF class of `fppd` on all three deployed hosts to settle the full `GOARCH`/`GOARM` matrix. **Read-only and safe against the live fleet.** Record both detection methods from §5.3 and confirm they agree.
2. Confirm on 9.4 that the install script sees `$FPPDIR` empty as an environment variable and set positionally.
3. Confirm a 0644 install script is silently skipped with nothing surfaced in the UI.
4. Confirm the uninstall hook's non-zero exit is swallowed and the directory removed anyway.
5. Against the bench container: configure a `URL` command pointing at a stub returning `401`, fire it from a schedule entry and from a playlist-started preset, and confirm nothing appears in the log, no warning appears in the UI, and the schedule entry reports no failure. **This converts §7.2 from source-verified to bench-verified before any fallback design is committed to.**
6. Confirm `playlistCallback` fires `start` and `stop` with MultiSync disabled, and that the MultiSync sequence hooks do not. This is the fact ADR-013's mechanism sentence rests on.
7. Measure what a blocking `playlistCallback` costs, by forking a script that sleeps and observing whether the playlist transition stalls.
8. Subscribe read-only to `command/run` on the **bench** broker, fire a URL command with a token in the query string, and confirm the token appears in the payload. Never against the operator's live broker, and never a publish.
9. Run the registry linter against a candidate repository before submitting, since Best practice findings block a first listing.
10. Exercise fresh install, upgrade from a pinned older release, uninstall, unreachable release host, and upgrade across an FPP major.

**Could not determine, and each needs the hosts rather than more reading:** whether `fpp-player` runs a 32-bit or 64-bit userspace, which decides its `GOARCH`; the platform string and word size of the PocketBeagle2; the exact commit of `fpp-remote-a`'s master build, on which every "master only" statement here is conditional; and whether the `phone-home` check fires in practice against an agent connecting to an operator-owned broker.

**Revalidate** whenever FPP's plugin manager, the registry's requirements, or the fleet's FPP version changes. The fleet's expected move to a 9.x release or the FPP 10 beta is a material change that crosses the regime boundary in §2 and makes the install, upgrade, and architecture-detection conclusions stale on the day it happens.
