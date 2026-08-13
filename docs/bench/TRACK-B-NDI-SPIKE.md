# Track B spike: GStreamer to NDI to Resolume

[Build plan](../build/BUILD-PLAN.md) · [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [RES-004](../research/RES-004-virtual-matrix-renderer-performance.md) · [RES-005](../research/RES-005-ndi-vs-hdmi-transport.md) · [RES-006](../research/RES-006-linux-ndi-support.md)

Written 2026-08-13. **Run this before building any renderer.**

## What this is, and what it is not

This proves one thing: that frames can leave a GStreamer pipeline on the reference renderer hardware, cross the network as NDI, and arrive in Resolume in a usable state.

**It is throwaway.** No part of it becomes the renderer. There is no FSEQ, no virtual-matrix extraction, no surface model, no agent, and no ShowMesh code at all. It is a pipeline on a command line and a layer in Resolume.

**Why it comes first.** RES-004, RES-005, and RES-006 are all at L0, and [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) commits the entire projection path to NDI on the strength of intent rather than measurement. Track B builds weeks of work on top of that assumption. If NDI into Resolume has a problem on this hardware, **that has to surface in week one, not week five**, because the fallback is HDMI with capture and switching to it changes the node design, the hardware, and the cabling.

There is also a specific reason to distrust optimism here: projection did not work on the Raspberry Pis last season, which is why this path is being rebuilt on x86. That is evidence that this part of the system is where the difficulty lives.

## Hardware and software to record

Record every one of these with the results. RES-004 and RES-005 both require the parameters alongside the numbers, and a result without them cannot be compared to the next one.

- Renderer node: model, CPU, RAM, GPU, OS and kernel version.
- NIC and link speed on the renderer, and the switch it lands on.
- Resolume: exact version, and the machine it runs on.
- NDI runtime version, and where it was installed from.
- GStreamer version, and the NDI plugin's name, version, and origin.
- Network path: same switch, routed, or wireless. Wired is the acceptance environment per RES-005.

## Operating system

**Debian 13 on the renderer node**, recommended 2026-08-13 and open to being overturned by the packaging check below.

The argument is media-stack friction, since that is what threatens the schedule. RHEL-family distributions do not carry the GStreamer Rust plugin set in their own repositories or in EPEL, so a Rocky node means building the NDI plugin from cargo and then owning that build; RHEL's media stack is also deliberately conservative. Those are the right tradeoffs for a server and the wrong ones for a show appliance that is rebuilt between seasons and has five weeks to work.

Debian is preferred over Ubuntu for reasons that are structural rather than taste. **FPP is Debian-based**, so the nodes match the platform this project already integrates with, and the Step 9 plugin targets that same family. **Raspberry Pi OS is Debian**, so the deferred ARM profile in RES-004 becomes a continuation rather than a second port. And Debian packages the GStreamer Rust plugins while staying current enough for VAAPI on the reference hardware's integrated graphics.

Ubuntu 24.04 LTS is the fallback if a packaging gap appears, and it carries the densest community documentation for NDI on Linux because the OBS and DistroAV ecosystem lives there.

**Check this before committing to it.** Confirm from `apt-cache search` and `gst-inspect-1.0` that the NDI element is actually present and loadable on the distribution you pick. The recommendation above is reasoned from packaging behaviour recalled rather than verified, which is precisely the kind of claim this project requires to be checked against the system's own output before it is built on. Ten minutes now, and record what you find in [RES-006](../research/RES-006-linux-ndi-support.md).

The coordinator is unaffected either way: it ships as a distroless container under [ADR-012](../decisions/ADR-012-docker-coordinator-deployment.md), so this is a node decision and not a fleet decision.

## Setup

The NDI runtime is **user-installed and never redistributed**, per the standing constraint and [RES-006](../research/RES-006-linux-ndi-support.md). Install it from NDI's own installer on the renderer node, and note the path and the runtime environment variable it wants.

The GStreamer NDI elements come from the Rust plugin set (`gst-plugins-rs`). **Confirm the actual element names from `gst-inspect-1.0` rather than from this document**, and write down what you find. This project has twice been bitten by a plan naming an external system's vocabulary from memory: FPP has no command called `Stop Playlist`, and the collector emits no signal called `fpp.status.player_state`. Both looked plausible until something ran. The sender element is expected to be an NDI sink, and expected is not the same as confirmed.

## The runs

Work up in this order and stop at the first one that fails, because a failure early makes the later runs meaningless.

**Run 1, does anything arrive.** A test pattern source into the NDI sink, at a modest resolution and frame rate. Confirm Resolume discovers the source by name and displays it. This is a yes or no.

**Run 2, the reference profile.** The same pipeline at 40 fps, at the canvas dimensions you actually intend to project. **This is the run that matters**, because ADR-026 decision 5 records 40 fps as a target nobody has measured, and RES-004 treats pixel count as a test parameter rather than a fixed resolution. Record achieved frame rate, not requested frame rate.

**Run 3, motion.** Replace the static pattern with high-motion content at the same dimensions. NDI's compression cost depends on what is moving, so a still image is the easy case and proves less than it appears to.

**Run 4, endurance.** Leave run 3 going for as long as you can stand, ideally the length of a show. Watch for frame rate decaying, memory climbing, and the machine heating up. RES-005's acceptance criterion is a representative full-show soak, and this is the cheap version of it.

**Run 5, recovery.** With the stream running, restart Resolume. Then restart the sender. Confirm both sides recover without hand-holding. Note how long each takes.

## What to record

For each run: achieved frame rate, whether pacing looked smooth or visibly stuttered, CPU load on the renderer, memory over time for run 4, and what Resolume showed. Note anything that looked wrong even if you cannot name it.

**Report jitter and dropped frames as observed results rather than hiding them behind an average frame rate**, which is RES-004's own instruction and matters because a stream averaging 40 fps while periodically hitching looks fine in a number and bad on a wall.

## How to read the outcome

**Runs 1 through 3 clean:** the transport assumption holds. Track B proceeds as planned, and RES-005 and RES-006 gain real evidence toward L2.

**Run 2 cannot hold 40 fps at your intended dimensions:** this is the most likely partial failure and it is not necessarily fatal. The lever is pixel count, which RES-004 deliberately left as a parameter. Find the dimensions the hardware does sustain and report that number, because the profile in ADR-026 is a target and this is the work that replaces it with a fact.

**Run 1 fails, or runs 4 and 5 show instability that cannot be tuned out:** stop and raise it. This is the case that changes the architecture. HDMI with capture is the recorded fallback and remains supported under ADR-026 decision 4, and choosing it would need ADR-026 revisited rather than worked around quietly.

## What this spike does not license

It does not raise RES-004 to a supported profile, because one bench on one machine is not a profile by that record's own acceptance criteria. It says nothing about multiple surfaces, ARM hardware, or HDMI. It says nothing about alignment between two simultaneous surfaces, which RES-005 lists separately and which needs two senders to test at all.

And it says nothing about a show. It is a test pattern on a wall.
