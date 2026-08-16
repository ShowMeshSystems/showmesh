#!/usr/bin/env bash
#
# Track B transport spike harness. See docs/bench/TRACK-B-NDI-SPIKE.md.
#
# Bench scaffolding, not product. No part of this becomes the renderer.
# It runs one GStreamer pipeline, logs what the pipeline actually achieved,
# samples the machine while it runs, and leaves a comparable artifact per run.

set -uo pipefail

# Job control on, so the backgrounded pipeline does not inherit SIGINT ignored.
# Without this, gst-launch can never be interrupted cleanly and never prints
# its final frame counters.
set -m

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_ROOT="${SPIKE_RESULTS_DIR:-$SCRIPT_DIR/results}"

RUN=""
NDI_NAME="ShowMesh-Spike"
WIDTH=1920
HEIGHT=1080
FPS=40
PATTERN=""
FILE=""
DURATION=""
SAMPLE_INTERVAL=10
SYNC=true
RECEIVER=false
LABEL=""

usage() {
  cat <<'EOF'
Usage: run-spike.sh --run <1..5> [options]

Runs, per docs/bench/TRACK-B-NDI-SPIKE.md. Work up in order and stop at the
first failure; a failure early makes the later runs meaningless.

  1  discovery       1280x720 @ 30, static bars. Does anything arrive at all.
  2  reference       1920x1080 @ 40 by default. The run that matters.
  3  motion          Reference dimensions with moving content.
  4  endurance       Run 3, left going. Defaults to 4 hours.
  5  recovery        Run 3 dimensions, no duration. You restart things by hand.

Options:
  --run N              Run number (required unless --receiver).
  --name NAME          NDI source name. Default: ShowMesh-Spike
  --width N            Override width.
  --height N           Override height.
  --fps N              Override frame rate.
  --pattern NAME       videotestsrc pattern. Default depends on run.
  --file PATH          Use real content instead of videotestsrc. Preferred for
                       runs 3 and 4; synthetic noise is an upper bound on
                       compression cost, not show content.
  --duration SECONDS   Stop after this long. Default: run 4 only, 14400.
  --sample-interval N  Seconds between machine samples. Default: 10
  --no-sync            Set sync=false on the sink. Read the warning below.
  --receiver           Run as an NDI receiver instead of a sender, to get a
                       measured number from the far side.
  --label TEXT         Extra text in the results directory name.
  -h, --help           This.

On sync: the sink defaults to sync=true here on purpose. With sync=false the
sink applies no QoS, so it can never report a late or dropped frame, and
"dropped: 0" then means nothing at all. Use --no-sync only if you have a
reason, and record that you did.

Output lands in results/<run>-<timestamp>/:
  environment.txt   what this machine is, captured automatically
  pipeline.txt      the exact pipeline that ran
  sender.log        timestamped gst-launch -v output, one measurement/second
  system.log        CPU, RSS, clock frequency and temperatures over time
  summary.txt       frame rate distribution, written when the run ends
  NOTES.md          the fields only you can fill in
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --run) RUN="$2"; shift 2 ;;
    --name) NDI_NAME="$2"; shift 2 ;;
    --width) WIDTH="$2"; shift 2 ;;
    --height) HEIGHT="$2"; shift 2 ;;
    --fps) FPS="$2"; shift 2 ;;
    --pattern) PATTERN="$2"; shift 2 ;;
    --file) FILE="$2"; shift 2 ;;
    --duration) DURATION="$2"; shift 2 ;;
    --sample-interval) SAMPLE_INTERVAL="$2"; shift 2 ;;
    --no-sync) SYNC=false; shift ;;
    --receiver) RECEIVER=true; shift ;;
    --label) LABEL="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# Run presets. Anything passed explicitly above wins.
if [[ "$RECEIVER" == false ]]; then
  case "$RUN" in
    1) : "${PATTERN:=smpte}"; [[ "$WIDTH" == 1920 ]] && WIDTH=1280 && HEIGHT=720 && FPS=30 ;;
    2) : "${PATTERN:=smpte}" ;;
    3) : "${PATTERN:=snow}" ;;
    4) : "${PATTERN:=snow}"; : "${DURATION:=14400}" ;;
    5) : "${PATTERN:=snow}" ;;
    "") echo "--run is required (or use --receiver). See --help." >&2; exit 2 ;;
    *) echo "unknown run: $RUN. Expected 1 through 5." >&2; exit 2 ;;
  esac
fi

for tool in gst-launch-1.0 gst-inspect-1.0; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing: $tool" >&2; exit 3; }
done

# Fail loudly on a missing element rather than inside a GStreamer parse error.
# The spike doc's own instruction: confirm the vocabulary from gst-inspect.
require_element() {
  gst-inspect-1.0 "$1" >/dev/null 2>&1 || {
    echo "GStreamer element '$1' not found on this machine." >&2
    echo "Run 'gst-inspect-1.0 | grep -i ndi' and record the real names in RES-006." >&2
    exit 3
  }
}

if [[ "$RECEIVER" == true ]]; then
  require_element ndisrc
  require_element ndisrcdemux
else
  require_element ndisink
  require_element videotestsrc
fi

RUN_TAG="$([[ "$RECEIVER" == true ]] && echo receiver || echo "run$RUN")"
[[ -n "$LABEL" ]] && RUN_TAG="$RUN_TAG-$LABEL"
RUN_DIR="$RESULTS_ROOT/$RUN_TAG-$(date +%Y%m%dT%H%M%S)"
mkdir -p "$RUN_DIR"

# BSD date rejects the GNU -Is spelling, and receiver mode may run on a Mac.
now() { date +%Y-%m-%dT%H:%M:%S%z; }

# Prefer ts, fall back to perl, degrade to unstamped rather than failing.
timestamp_filter() {
  if command -v ts >/dev/null 2>&1; then
    ts '[%Y-%m-%d %H:%M:%S]'
  elif command -v perl >/dev/null 2>&1; then
    perl -ne 'BEGIN{$|=1} use POSIX qw(strftime); print strftime("[%Y-%m-%d %H:%M:%S] ", localtime), $_'
  else
    cat
  fi
}

capture_environment() {
  {
    echo "# Captured $(now) on $(hostname)"
    echo
    echo "## OS"
    uname -a
    [[ -r /etc/os-release ]] && cat /etc/os-release
    echo
    echo "## CPU / memory"
    if [[ -r /proc/cpuinfo ]]; then
      grep -m1 'model name' /proc/cpuinfo
      echo "cores: $(grep -c ^processor /proc/cpuinfo)"
    else
      sysctl -n machdep.cpu.brand_string 2>/dev/null
    fi
    free -h 2>/dev/null || vm_stat 2>/dev/null | head -3
    echo
    echo "## GPU"
    lspci 2>/dev/null | grep -iE 'vga|display|3d' || echo "lspci unavailable"
    echo
    echo "## Network"
    local iface
    iface="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}' | head -1)"
    if [[ -n "$iface" ]]; then
      echo "default interface: $iface"
      ethtool "$iface" 2>/dev/null | grep -iE 'speed|duplex|link detected' || echo "ethtool unavailable"
    else
      echo "default interface: unknown"
    fi
    echo
    echo "## GStreamer"
    gst-launch-1.0 --version
    echo
    echo "### ndisink"
    gst-inspect-1.0 ndisink 2>/dev/null | grep -E '^ *(Filename|Version|Library|Source module|Binary package|Origin)' \
      || echo "not present (receiver-only machine?)"
    echo
    echo "### ndisrc"
    gst-inspect-1.0 ndisrc 2>/dev/null | grep -E '^ *(Filename|Version|Library|Source module|Binary package|Origin)' \
      || echo "not present"
    echo
    echo "## NDI runtime environment"
    env | grep -i ndi || echo "no NDI environment variables set"
  } > "$RUN_DIR/environment.txt" 2>&1
}

write_notes_template() {
  cat > "$RUN_DIR/NOTES.md" <<EOF
# $RUN_TAG

The spike doc requires these alongside the numbers. A result without them
cannot be compared to the next one. Fill them in before you close the terminal.

- Resolume version:
- Machine Resolume runs on:
- NDI runtime version, and where it was installed from:
- Network path (same switch / routed / wireless):
- Switch the renderer lands on:

## What Resolume showed

- Did it discover the source by name:
- Did it display:
- Its own reported frame rate, if any:

## Pacing, by eye

Smooth, or visibly stuttering? Note anything that looked wrong even if you
cannot name it.

## Anything else
EOF
}

sample_system() {
  local pid="$1"
  while kill -0 "$pid" 2>/dev/null; do
    local cpu_rss khz temps
    cpu_rss="$(ps -o pcpu=,rss= -p "$pid" 2>/dev/null | tr -s ' ')"
    khz="$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq 2>/dev/null || echo NA)"
    temps="$(sensors 2>/dev/null | grep -oE '\+[0-9]+\.[0-9]+°C' | tr '\n' ' ')"
    echo "$(now) pid=$pid cpu_rss=[${cpu_rss# }] cpu0_khz=$khz temps=[${temps}]"
    sleep "$SAMPLE_INTERVAL"
  done
}

# Frame rate distribution, not just an average. A stream averaging 40 fps
# while periodically hitching looks fine in a number and bad on a wall.
write_summary() {
  {
    echo "# $RUN_TAG"
    echo "pipeline: $(cat "$RUN_DIR/pipeline.txt")"
    echo
    local vals
    vals="$(grep -oE 'current: [0-9.]+' "$RUN_DIR/sender.log" 2>/dev/null | grep -oE '[0-9.]+')"
    if [[ -z "$vals" ]]; then
      echo "No frame rate measurements in sender.log."
      echo "If the pipeline ran, check that -v was passed and the sink is fpsdisplaysink."
    else
      echo "## Achieved frame rate, one sample per second"
      printf '%s\n' "$vals" | sort -n | awk '
        {v[NR]=$1; s+=$1}
        END {
          printf "samples   %d\n", NR
          printf "min       %.2f\n", v[1]
          printf "p05       %.2f\n", v[int(NR*0.05)+1]
          printf "median    %.2f\n", v[int((NR+1)/2)]
          printf "mean      %.2f\n", s/NR
          printf "max       %.2f\n", v[NR]
        }'
      echo
      echo "min and p05 are the numbers that matter. Report them, not the mean."
    fi
    echo
    echo "## Final sink counters"
    grep -oE 'rendered: [0-9]+, dropped: [0-9]+' "$RUN_DIR/sender.log" 2>/dev/null | tail -1 \
      || echo "none recorded"
    if [[ "$SYNC" == false ]]; then
      echo
      echo "WARNING: ran with sync=false, so the sink applied no QoS and the"
      echo "dropped count above is structurally zero. It is not evidence."
    fi
    echo
    echo "## Errors and warnings"
    grep -iE 'error|warning|dropping|late' "$RUN_DIR/sender.log" 2>/dev/null | head -40 \
      || echo "none"
  } > "$RUN_DIR/summary.txt" 2>&1
}

GST_PID=""
SAMPLER_PID=""

cleanup() {
  [[ -n "$SAMPLER_PID" ]] && kill "$SAMPLER_PID" 2>/dev/null
  # SIGINT so gst-launch sends EOS and prints its final counters. SIGKILL only
  # if it will not go, because a killed sender can keep holding the NDI name.
  if [[ -n "$GST_PID" ]] && kill -0 "$GST_PID" 2>/dev/null; then
    kill -INT "$GST_PID" 2>/dev/null
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$GST_PID" 2>/dev/null || break
      sleep 1
    done
    kill -0 "$GST_PID" 2>/dev/null && { echo "pipeline did not stop, killing"; kill -9 "$GST_PID" 2>/dev/null; }
  fi
  sleep 1
  write_summary
  echo
  echo "Results: $RUN_DIR"
  echo
  cat "$RUN_DIR/summary.txt"
  echo
  echo "Fill in $RUN_DIR/NOTES.md while it is fresh."
}
trap cleanup EXIT INT TERM

# Built as an argv array, never a string through eval: eval leaves gst-launch a
# grandchild, so $! is a wrapper and Ctrl-C never reaches the pipeline.
if [[ "$RECEIVER" == true ]]; then
  ARGS=( -v ndisrc "ndi-name=$NDI_NAME" '!' ndisrcdemux name=d d.video
         '!' queue '!' videoconvert
         '!' fpsdisplaysink text-overlay=false sync=false fps-update-interval=1000 )
else
  if [[ -n "$FILE" ]]; then
    [[ -r "$FILE" ]] || { echo "cannot read --file $FILE" >&2; exit 2; }
    ARGS=( -v filesrc "location=$FILE" '!' decodebin '!' videoconvert '!' videoscale '!' videorate )
  else
    ARGS=( -v videotestsrc is-live=true "pattern=$PATTERN" )
  fi
  ARGS+=( '!' "video/x-raw,format=UYVY,width=$WIDTH,height=$HEIGHT,framerate=$FPS/1"
          '!' fpsdisplaysink "video-sink=ndisink ndi-name=$NDI_NAME"
          text-overlay=false "sync=$SYNC" fps-update-interval=1000 )
fi

PIPELINE="gst-launch-1.0 ${ARGS[*]}"
echo "$PIPELINE" > "$RUN_DIR/pipeline.txt"
capture_environment
write_notes_template

echo "Run:      $RUN_TAG"
echo "Results:  $RUN_DIR"
echo "Pipeline: $PIPELINE"
[[ -n "$DURATION" ]] && echo "Duration: ${DURATION}s" || echo "Duration: until Ctrl-C"
echo

gst-launch-1.0 "${ARGS[@]}" > >(timestamp_filter | tee "$RUN_DIR/sender.log") 2>&1 &
GST_PID=$!

sleep 2
kill -0 "$GST_PID" 2>/dev/null || { echo "pipeline exited immediately, see sender.log" >&2; exit 4; }

sample_system "$GST_PID" > "$RUN_DIR/system.log" &
SAMPLER_PID=$!

if [[ -n "$DURATION" ]]; then
  SECONDS_LEFT="$DURATION"
  while [[ "$SECONDS_LEFT" -gt 0 ]] && kill -0 "$GST_PID" 2>/dev/null; do
    sleep 1
    SECONDS_LEFT=$((SECONDS_LEFT - 1))
  done
else
  wait "$GST_PID"
fi
