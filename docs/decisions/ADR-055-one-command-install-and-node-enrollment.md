# ADR-055: One-command install and node enrollment

Status: Accepted (owner, 2026-09-23)
Date: 2026-09-23

## Context

Installing ShowMesh today means reading `deploy/README.md` and
`deploy/node/README.md` end to end, running several scripts by hand on two
kinds of machine, copying a broker password printed once on the coordinator
host into a file on the node, minting a machine principal and an admin-role
token with `showmeshctl`, pasting that too, and building the NDI GStreamer
plugin from `gst-plugins-rs` with a toolchain the node otherwise does not
need. Every one of those steps is a place a first install fails, and most of
them fail quietly: a node with no API token accepts every FPP Connect upload
and registers none of them.

The target environment is narrow and known. Nodes run Debian 13 on amd64 or
arm64. The coordinator runs as a Docker Compose bundle from published images.
That makes an installer that does the whole job practical, with one exception
it must not automate: the NDI runtime, which ShowMesh may not redistribute
([ADR-010](ADR-010-apache-2-license.md), [RES-006](../research/RES-006-linux-ndi-support.md)).

[ADR-024](ADR-024-identity-authorization-and-audit.md) section 0 left node
enrollment undecided and recorded one constraint for whoever built it:
delivering broker credentials from the coordinator must not give a rebooting
node a boot-time dependency on the coordinator. This record decides
enrollment and carries that constraint as decision 6.

## Decision

### 1. One installer, one command per machine

`showmesh-install` installs one machine in one of four roles: coordinator,
render node, audio node, or coordinator and node on one host. It is
interactive by default and takes flags for an unattended install. It is safe
to re-run: a second run upgrades in place and keeps configuration and state,
the way `deploy/node/install.sh` already does.

It wraps `deploy/node/preflight.sh`, `deploy/node/install.sh` and
`deploy/node/install-ptp-audio.sh` rather than replacing them. Their refusals
(the Debian 13 floor, the shared-library symbol check, the service-account
guards) stay where they are and keep working when run by hand.

It is delivered as release files: a short bootstrap script,
`get-showmesh.sh`, and a versioned installer bundle holding the installer and
the Compose bundle. Both are covered by the release's `SHA256SUMS`. The
bootstrap verifies the bundle's checksum before it runs anything from it. The
documented install is one line:

```sh
curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/<tag>/get-showmesh.sh | sudo bash
```

The tag is written into the URL rather than resolved through GitHub's
`latest` alias, because `latest` never resolves to a pre-release.

The installer finishes only on evidence that post-dates the install: for a
node, the node reporting to the coordinator; for a coordinator, its API
answering and its broker accepting a login.

### 2. The NDI runtime is the user's download

The installer never downloads the NDI runtime. On a render node it explains
where to get the Linux runtime and what licence the user is accepting, then
asks for the downloaded file's path, installs the library from it and confirms
`ndisink` resolves. The user may skip this step. A node installed without the
runtime starts and keeps every other capability, which is what
[ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) decision
6 already requires, and `showmesh-install --ndi <file>` adds it later.

### 3. The NDI GStreamer plugin is built by the release workflow

The release workflow builds the NDI plugin from a pinned `gst-plugins-rs`
revision against Debian 13's GStreamer, for amd64 and arm64, and ships the
resulting `.so` in the node agent package. The pinned revision and the build
commands live in the repository, so a node is reproducible without the bench
machine.

This amends the owner decision of 2026-08-16 recorded in RES-006, which was to
compile the plugin on each node. That decision's accepted downsides were a
cargo toolchain on every node and a plugin with no recorded provenance; this
removes both. It changes nothing in ADR-010: the plugin contains no NDI SDK
code and loads the runtime with `dlopen` at run time.

The installer may still compile the plugin on the node when no prebuilt plugin
exists for its architecture. That is a fallback, not the normal path.

### 4. A node joins with a one-time enrollment code

An operator holding the new scope `node:enroll` mints an enrollment code on
the coordinator for a node ID chosen at that moment. The coordinator checks
the ID against the node-ID rule in `pkg/mqttproto` and refuses one reserved
for a fixed broker role. The code:

- works once;
- expires, 15 minutes by default and never later than 24 hours;
- is shown once and stored only as a hash;
- can be cancelled before it is used.

The installer sends the code to an unauthenticated redeem endpoint. The code is
the credential for that one call. This adds a third credential form to ADR-024
decision 1, alongside passwords and tokens, and it is deliberately the
narrowest: it can do one thing, once, for one named node. Every redemption is
audited and attributed to the principal who minted the code. The coordinator
limits failed redemptions, so a short code cannot be guessed.

A code minted for a node that is already enrolled is refused unless it was
minted as a re-enrollment. Redeeming a re-enrollment code replaces the node's
broker password and revokes its previous API token. This is also how a node's
credentials are rotated.

### 5. What a redemption returns

A redemption returns, once:

- the node ID;
- the broker address as a node reaches it, which is not the address the
  coordinator uses inside its own container;
- the node's broker login (decision 7 or 8);
- the coordinator's API address;
- an API token for a new machine principal holding the new `node` role.

The `node` role carries exactly the scopes the agent's own calls to the
coordinator need, which today means FPP Connect upload registration and asset
reads. That is narrower than the admin-role token `deploy/node/agent.env.example`
tells an installer to mint today. Hand-provisioned admin tokens keep working.

### 6. Enrollment happens at install time only

The installer writes what the redemption returned into
`/etc/showmesh/agent.env` and nothing else changes on the node. The agent
reads that file at start, exactly as it does today. A rebooting node never
contacts the coordinator for credentials, so enrollment adds no boot-time
dependency, which is the constraint ADR-024 recorded.

### 7. The built-in broker is the default

On the built-in broker, a redemption provisions the node's own login the same
way `deploy/mosquitto/add-agent-credential.sh` does: a username equal to the
node ID, a fresh password, and the node's own block in the generated access
list. The coordinator writes the same password file and generated access list
the scripts write, following the same rules, and the broker reloads them when
they change. The scripts keep working, and either may run after the other.

### 8. An external broker is supported and left alone

The coordinator installer may instead point ShowMesh at a broker the operator
already runs. ShowMesh then does not create users or access rules on it. Every
enrolled node receives one shared login the operator configures, or no login
when that broker allows anonymous clients. The installer states plainly, before
the operator confirms, that on such a broker any client that can reach it can
command every node, and that the built-in broker's per-node access list does
not apply. The built-in broker stays the default and needs no broker knowledge
to install.

### 9. The installer's output follows the operator copy rule

Everything the installer prints states the fact and then the action, in plain
words, per section 5.1 of the UI design guide. A refusal names the exact
command that fixes it. Decorative output (animation, colour effects, hidden
modes) runs only when the operator asks for it with `--party`, only on the
opening and success screens, and never in front of a refusal or an error.

## Consequences

- A first install needs no reading beyond the one command, plus the NDI
  download on a render node.
- The coordinator gains write access to the broker's password file and
  generated access list. A coordinator compromise already carried command
  authority over every node ([ADR-024](ADR-024-identity-authorization-and-audit.md)
  records that exposure); it now also carries the ability to create broker
  logins.
- The redeem endpoint is the only write the API accepts without a principal.
  Its failure limit is what makes a short code safe; it must not be removed as
  an inconvenience.
- A redemption carries a broker password and an API token over the
  coordinator's HTTP API. On a network where that traffic can be captured,
  install-time capture reveals them. This is accepted for dedicated show
  networks; TLS on the coordinator removes it.
- The release job grows by the plugin build on both architectures.
- RES-006's open item to record the plugin build recipe is closed by
  decision 3's pinned revision.

## Alternatives

**Paste credentials into the installer.** Kept as the fallback when the
coordinator cannot be reached, and rejected as the normal path, because it
keeps every manual step this record exists to remove.

**Deliver credentials from the coordinator at every node boot.** Rejected by
ADR-024's recorded constraint: a node that cannot boot into a working state
without the coordinator turns a coordinator outage after a power cut into a
dark show.

**Mosquitto's dynamic security plugin.** It would let the coordinator manage
logins over MQTT with no files. Rejected for now because it replaces the
password file and access list every existing installation runs, and a
migration of the live broker's authentication is the wrong change to make
beside a first release.

**Download the NDI runtime for the user.** Rejected by ADR-010 and RES-006.

## Related

- [ADR-010](ADR-010-apache-2-license.md), NDI runtime licensing
- [ADR-024](ADR-024-identity-authorization-and-audit.md), identity, broker
  credentials, and the enrollment constraint
- [ADR-026](ADR-026-renderer-surface-model-and-reference-transport.md) decision
  6, a node without the NDI runtime
- [RES-006](../research/RES-006-linux-ndi-support.md), Linux NDI support and
  the 2026-08-16 plugin build decision this amends
