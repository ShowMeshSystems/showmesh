#!/usr/bin/env python3
"""Bench-only stand-in for the coordinator's enrollment redeem and node status read.

Usage: fake_enrollment_server.py PORT GOOD_CODE NODE_ID LOG_FILE
GOOD_CODE redeems once, then answers 410. EXPD-0000 always answers 410 and
anything else 404, each with an RFC 7807 detail. Every request is logged.
"""
import base64
import json
import os
import sys
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
GOOD_CODE = sys.argv[2].replace("-", "").upper()
NODE_ID = sys.argv[3]
LOG = sys.argv[4]
TOKEN = "bench-api-token-" + NODE_ID
PUBLIC_KEY = base64.b64encode(bytes(range(32))).decode()
state = {"used": False}


def now():
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        with open(LOG, "a") as f:
            f.write("%s %s\n" % (self.command, self.path))

    def send(self, status, body, content_type="application/json"):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def problem(self, status, title, detail):
        self.send(status, {"type": "about:blank", "title": title, "status": status, "detail": detail},
                  "application/problem+json")

    def do_GET(self):
        if self.path == "/healthz":
            return self.send(200, {"status": "ok"})
        prefix = "/api/v1/nodes/"
        if self.path.startswith(prefix):
            if self.headers.get("Authorization") != "Bearer " + TOKEN:
                return self.problem(401, "Unauthorized", "This request needs a valid token.")
            if self.path[len(prefix):] != NODE_ID or not state["used"]:
                return self.problem(404, "Not Found", "No node with that ID has reported.")
            return self.send(200, {"serverTime": now(), "node": {
                "nodeId": NODE_ID, "startedAt": now(),
                "controlPlane": {"state": "online", "reason": None}}})
        self.problem(404, "Not Found", "Nothing is served at this path.")

    def do_POST(self):
        if self.path != "/api/v1/node-enrollments/redeem":
            return self.problem(404, "Not Found", "Nothing is served at this path.")
        length = int(self.headers.get("Content-Length", "0"))
        try:
            body = json.loads(self.rfile.read(length))
            code = str(body["code"]).replace("-", "").upper()
            assert body["hostname"] and body["arch"] in ("amd64", "arm64")
        except Exception:
            return self.problem(400, "Bad Request", "The request needs a code, a hostname and an architecture.")
        if code == "EXPD0000" or (code == GOOD_CODE and state["used"]):
            return self.problem(410, "Gone", "This enrollment code has expired or was already used. Ask for a new one with: showmeshctl node enroll <node-id>")
        if code != GOOD_CODE:
            return self.problem(404, "Not Found", "No enrollment code matches the one given. Check it, or ask for a new one with: showmeshctl node enroll <node-id>")
        state["used"] = True
        self.send(200, {
            "nodeId": NODE_ID,
            "brokerUrl": "tcp://192.0.2.10:1883",
            "mqttUsername": NODE_ID,
            "mqttPassword": base64.urlsafe_b64encode(os.urandom(18)).decode(),
            "apiToken": TOKEN,
            "coordinatorUrl": "http://127.0.0.1:%d" % PORT,
            "coordinatorPublicKey": PUBLIC_KEY,
        })


HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
