#!/usr/bin/env python3
"""Answers GET /api/v1/night/session with a fixed state, for the coordinator bench.

Usage: fake_night_server.py PORT STATE
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

STATE = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/api/v1/night/session":
            self.send_response(404)
            self.end_headers()
            return
        body = json.dumps({"serverTime": "2026-09-28T00:00:00Z", "session": {"id": "bench-night", "state": STATE}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
