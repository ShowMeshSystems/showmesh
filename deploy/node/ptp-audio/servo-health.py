#!/usr/bin/env python3
# Extracts PTP servo samples (offset, state, frequency, path delay) from
# ptp4l/phc2sys log lines on stdin. One regex covers both label sets:
# ptp4l's follower sync ("master offset ... freq ... path delay ...") and
# phc2sys's own PHC-from-system-clock discipline ("... offset ... freq ...
# delay ..."). Facts only, one SAMPLE= line per matched log line, oldest
# first: verify-ptp-audio.sh applies the health thresholds and prints them.
#
# Reads on stdin, never an argument, so a fixture file or `journalctl`
# output of any size feeds it the same way, matching driver-election.py's
# own stdin convention.
import re
import sys

LINE_RE = re.compile(
    r'offset\s+(-?\d+)\s+s(\d+)\s+freq\s+(-?\d+)\s+(?:path\s+)?delay\s+(-?\d+)'
)

count = 0
for line in sys.stdin:
    m = LINE_RE.search(line)
    if not m:
        continue
    offset, state, freq, delay = m.groups()
    print(f"SAMPLE={offset}|{state}|{freq}|{delay}")
    count += 1

print(f"SAMPLE_COUNT={count}")
