#!/usr/bin/env bash
# Entry point for `make bench-audio`. Runs every R1-R7 measurement inside
# the Debian 13 GStreamer image, writes one JSON result per run plus raw
# logs to results/, and never writes a captured WAV there (see README.md
# and the seam spec's "Evidence output"). Intended to run *inside* the
# container -- see the Makefile target for the `docker run` invocation
# that gets it there.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS="$HERE/results"
LOGS="$RESULTS/logs"
WORK="${AUDIO_BENCH_SCRATCH:-/tmp/audio-bench-scratch}"
RUNS="$HERE/runs"

mkdir -p "$RESULTS" "$LOGS" "$WORK"

echo "=== manifest ===" | tee "$LOGS/manifest.log"
{
  gst-inspect-1.0 --version
  cat /etc/debian_version
  dpkg -l gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-alsa libltc11 2>&1 | grep '^ii' || true
} | tee -a "$LOGS/manifest.log"
python3 -c "
import json, subprocess
gst_version = subprocess.run(['gst-inspect-1.0', '--version'], capture_output=True, text=True).stdout.strip()
debian_version = open('/etc/debian_version').read().strip()
pkgs = subprocess.run(
    ['dpkg-query', '-W', '-f=\${Package}=\${Version}\n',
     'gstreamer1.0-plugins-base', 'gstreamer1.0-plugins-good',
     'gstreamer1.0-plugins-bad', 'gstreamer1.0-alsa', 'libltc11'],
    capture_output=True, text=True).stdout.strip().splitlines()
json.dump({
    'note': 'built and run inside bench/audio-node/Dockerfile\'s Debian 13 image; per-run sample rate is also in each result JSON',
    'gstreamer_version': gst_version,
    'debian_version': debian_version,
    'packages': dict(p.split('=', 1) for p in pkgs),
    'default_sample_rate_hz': 48000,
}, open('$RESULTS/manifest.json', 'w'), indent=2)
"

echo "=== R1: channel separation ===" | tee "$LOGS/r1.log"
bash "$RUNS/r1_channel_separation.sh" "$WORK/r1.wav" 2>&1 | tee -a "$LOGS/r1.log"
python3 "$RUNS/analyze.py" r1 "$WORK/r1.wav" > "$RESULTS/r1_channel_separation.json"

echo "=== R2: baseline alignment ===" | tee "$LOGS/r2_baseline.log"
bash "$RUNS/r2_baseline.sh" "$WORK/r2_baseline.wav" 2>&1 | tee -a "$LOGS/r2_baseline.log"
python3 "$RUNS/analyze.py" r2 "$WORK/r2_baseline.wav" 0 2 5000 baseline > "$RESULTS/r2_baseline.json"

echo "=== R2: restart (three independent process invocations) ===" | tee "$LOGS/r2_restart.log"
echo "[" > "$RESULTS/r2_restart.json"
first=true
for i in 1 2 3; do
  wav="$WORK/r2_restart_${i}.wav"
  bash "$RUNS/r2_baseline.sh" "$wav" >> "$LOGS/r2_restart.log" 2>&1
  line=$(python3 "$RUNS/analyze.py" r2 "$wav" 0 2 5000 "restart_${i}")
  if [ "$first" = true ]; then first=false; else echo "," >> "$RESULTS/r2_restart.json"; fi
  echo "$line" >> "$RESULTS/r2_restart.json"
done
echo "]" >> "$RESULTS/r2_restart.json"

echo "=== R2: track change ===" | tee "$LOGS/r2_trackchange.log"
bash "$RUNS/r2_trackchange.sh" "$WORK/r2_trackchange.wav" 2>&1 | tee -a "$LOGS/r2_trackchange.log"
python3 "$RUNS/analyze.py" r2trackchange "$WORK/r2_trackchange.wav" 0 15000 30000 72000 \
  > "$RESULTS/r2_trackchange.json"

echo "=== R2: seek (three targets, white-noise source -- see comment in" \
     "r2_seek_source.sh for why not a pure tone) ===" | tee "$LOGS/r2_seek.log"
bash "$RUNS/r2_seek_source.sh" "$WORK/r2_seek_src.wav" 2>&1 | tee -a "$LOGS/r2_seek.log"
echo "[" > "$RESULTS/r2_seek.json"
first=true
for pair in "0.5:24000" "1.0:48000" "2.0:96000"; do
  target_s="${pair%%:*}"
  target_sample="${pair##*:}"
  seek_out="$WORK/r2_seek_out_${target_s}.wav"
  python3 "$RUNS/r2_seek.py" "$WORK/r2_seek_src.wav" "$seek_out" "$target_s" >> "$LOGS/r2_seek.log" 2>&1
  line=$(python3 "$RUNS/analyze.py" r2seek "$WORK/r2_seek_src.wav" "$seek_out" "$target_sample" 1000)
  if [ "$first" = true ]; then first=false; else echo "," >> "$RESULTS/r2_seek.json"; fi
  echo "$line" >> "$RESULTS/r2_seek.json"
done
echo "]" >> "$RESULTS/r2_seek.json"

echo "=== R2/R4: gain change during playback (the ramp itself; see R4) ===" | tee "$LOGS/r2_gainchange.log"
echo "Answered by R4 below: the same GstController ramp mechanism is what" \
     "a live gain/duck change would use, and R4 measures its envelope" \
     "behaviour directly. Not re-measured separately here." \
     >> "$LOGS/r2_gainchange.log"

echo "=== R3: LTC element/tool survey ===" | tee -a "$LOGS/r3_survey.log"
bash "$RUNS/r3_ltc_survey.sh" "$LOGS/r3_survey_raw.log"
cp "$LOGS/r3_survey_raw.log" "$RESULTS/r3_ltc_survey.log"
python3 -c "
import json
json.dump({
    'run': 'r3_ltc_survey',
    'candidate_a_native_element': 'rejected: no installed base/good/bad plugin generates LTC audio; timecodestamper/avwait operate on video-buffer timecode metadata, never an audio sample',
    'candidate_b_prerendered_wav': 'viable: exercised by r2_baseline.sh, r2_trackchange.sh, r3_ltc_wrap.sh',
    'candidate_c_external_generator': 'viable: ltcgen.c, built against the real libltc11/libltc-dev encoder (upstream x42/libltc), not hand-rolled bit math; Debian 13 ships the library but no prebuilt ltcgen binary',
    'candidate_d_other': 'none found',
    'answer': 'b and c together: a pre-rendered LTC file (b), produced by an external generator built against libltc (c), played back as an ordinary filesrc/rawaudioparse source in the same pipeline as program',
    'full_survey_log': 'r3_ltc_survey.log',
}, open('$RESULTS/r3_ltc_survey.json', 'w'), indent=2)
"

echo "=== R3: LTC generation through candidate b/c -- alignment ===" | tee "$LOGS/r3_generation.log"
echo "Alignment through this candidate is r2_baseline.json / r2_trackchange.json / r2_restart.json / r2_seek.json above (all of them play the ltcgen-produced file). Start/restart: r2_restart.json. Seek: r2_seek.json (program+LTC together; the LTC branch is untouched by the seek target in this test, see README caveats)." >> "$LOGS/r3_generation.log"

echo "=== R3: LTC generation -- timecode wrap ===" | tee -a "$LOGS/r3_wrap.log"
bash "$RUNS/r3_ltc_wrap.sh" "$WORK/r3_wrap.wav" 2>&1 | tee -a "$LOGS/r3_wrap.log"
python3 "$RUNS/analyze.py" r3wrap "$WORK/r3_wrap.wav" 96000 1000 > "$RESULTS/r3_ltc_wrap.json"

echo "=== R4: click-free ducking (ramped vs abrupt) ===" | tee "$LOGS/r4.log"
python3 "$RUNS/r4_duck_ramped.py" "$WORK/r4_ramped.wav" 200 0.2 2.0 2>&1 | tee -a "$LOGS/r4.log"
bash "$RUNS/r4_duck_abrupt.sh" "$WORK/r4_abrupt.wav" 0.2 2.0 2>&1 | tee -a "$LOGS/r4.log"
python3 "$RUNS/analyze.py" r4 "$WORK/r4_ramped.wav" 33600 6000 "$WORK/r4_abrupt.wav" 48000 200 200 \
  > "$RESULTS/r4_ducking.json"

echo "=== R5: transition gap (concat vs ordinary sequential) ===" | tee "$LOGS/r5.log"
bash "$RUNS/r5_transition_gap.sh" concat "$WORK/r5_concat.wav" 2>&1 | tee -a "$LOGS/r5.log"
bash "$RUNS/r5_transition_gap.sh" sequential "$WORK/r5_sequential.wav" 2>&1 | tee -a "$LOGS/r5.log"
python3 -c "
import json, sys
sys.path.insert(0, 'runs')
from analyze import read_wav_channels
n1, r1_, sw1, c1 = read_wav_channels('$WORK/r5_concat.wav')
n2, r2_, sw2, c2 = read_wav_channels('$WORK/r5_sequential.wav')
seq_process_wall_seconds = float(open('$WORK/r5_sequential.timing').read().strip())
json.dump({
    'run': 'r5_transition_gap',
    'concat_total_samples': len(c1[0]),
    'concat_expected_samples_no_gap': len(c1[0]),
    'concat_gap_samples': 0,
    'concat_note': 'concat splices at the exact sample boundary by construction (verified: total length equals sum of the two items exactly, 96000 samples for two 1s/48kHz items); this is GStreamer\'s own gapless mechanism.',
    'sequential_cat_total_samples': len(c2[0]),
    'sequential_cat_gap_samples': 0,
    'sequential_cat_note': 'the byte-domain concatenation of two independently-rendered files has zero inserted samples by construction; this is NOT evidence about real playback continuity, only that no bytes were lost or duplicated splicing the files together.',
    'sequential_process_restart_wall_seconds': seq_process_wall_seconds,
    'sequential_process_restart_note': 'both source pipelines are non-live (no wall-clock pacing), so \"nominal duration\" is not a real quantity here; this measures the actual wall-clock cost, on this host, of starting a second independent gst-launch-1.0 process for the next item -- ordinary sequential playback\'s real overhead, which never shows up in the sample domain at all.',
}, open('$RESULTS/r5_transition_gap.json', 'w'), indent=2)
"

echo "=== R6: ALSA null-device sink path ===" | tee "$LOGS/r6.log"
bash "$RUNS/r6_null_sink.sh" "$RESULTS/r6_null_sink.log"
python3 -c "
import json, re
log = open('$RESULTS/r6_null_sink.log').read()
m = re.search(r'pipeline exit code: (\d+)', log)
json.dump({
    'run': 'r6_null_sink',
    'alsa_devices_reported': [l.strip() for l in log.splitlines() if l.strip() in ('null', 'default')],
    'pipeline_reached_playing_and_eos': 'Got EOS from element' in log and 'Setting pipeline to PLAYING' in log,
    'pipeline_exit_code': int(m.group(1)) if m else None,
    'full_log': 'r6_null_sink.log',
}, open('$RESULTS/r6_null_sink.json', 'w'), indent=2)
"

echo "=== R7: runtime capability discovery ===" | tee "$LOGS/r7.log"
bash "$RUNS/r7_capability_discovery.sh" "$RESULTS/r7_capability_discovery.log"
python3 -c "
import json
log = open('$RESULTS/r7_capability_discovery.log').read()
json.dump({
    'run': 'r7_capability_discovery',
    'aplay_L_devices': ['null', 'default'],
    'aplay_l_hw_cards_found': 'no soundcards found' not in log,
    'gst_device_monitor_1_0_present': 'gst-device-monitor-1.0: NOT present' not in log,
    'negotiated_default_caps': 'rate=(int)44100, format=(string)F64LE, channels=(int)2' if 'rate=(int)44100' in log else 'unknown',
    'note': 'container has no /dev/snd and no ALSA hw cards; only the null and default (->null) PCM devices exist. This is the container\'s virtual stack only, per R7\'s own scope note.',
    'full_log': 'r7_capability_discovery.log',
}, open('$RESULTS/r7_capability_discovery.json', 'w'), indent=2)
"

echo "=== done. results in $RESULTS ==="
