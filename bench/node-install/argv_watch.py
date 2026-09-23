#!/usr/bin/env python3
"""Bench-only: records every process whose command line holds a secret, until killed.

Usage: argv_watch.py OUT_FILE SECRET...
Reads /proc/*/cmdline, which is what ps prints, in a tight loop. The installer's
own command line and its bootstrap's may carry the code the operator typed, so
lines naming showmesh-install or starting with "bash -s" are not reported.
"""
import os
import signal
import sys

OUT = sys.argv[1]
SECRETS = [s.encode() for s in sys.argv[2:]]
ME = str(os.getpid())
seen = set()
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))

with open(OUT, "a") as out:
    while True:
        for pid in os.listdir("/proc"):
            if not pid.isdigit() or pid == ME:
                continue
            try:
                with open("/proc/%s/cmdline" % pid, "rb") as f:
                    cmd = f.read()
            except OSError:
                continue
            if b"showmesh-install" in cmd or cmd.startswith(b"bash\0-s\0"):
                continue
            for secret in SECRETS:
                if secret in cmd and (pid, cmd) not in seen:
                    seen.add((pid, cmd))
                    out.write("%s %s\n" % (pid, cmd.replace(b"\0", b" ").decode(errors="replace")))
                    out.flush()
