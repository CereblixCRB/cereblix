#!/usr/bin/env python3
"""Tiny mock Cereblix node for testing nmminer end-to-end (getwork + submitwork).
Returns an easy-ish target so the miner finds & submits shares quickly."""
import http.server, json, threading, time, sys

HEIGHT = int(sys.argv[1]) if len(sys.argv) > 1 else 6000
PORT   = int(sys.argv[2]) if len(sys.argv) > 2 else 18799
ZB     = int(sys.argv[3]) if len(sys.argv) > 3 else 1   # leading zero target bytes

header = bytes((i * 13 + 5) & 0xff for i in range(124))            # 124-byte header template
seed   = bytes((i * 7 + 1) for i in range(32))                     # epoch seed (matches bench)
# target: hash qualifies if its first ZB bytes are 0x00 (~1/256^ZB) -> rarer submits
target = bytes([0x00] * ZB + [0xff] * (32 - ZB))

submits = [0]

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _send(self, obj):
        b = json.dumps(obj).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers()
        self.wfile.write(b)
    def do_GET(self):
        if "/getwork" in self.path:
            self._send({"id": "job-%d" % int(time.time()),
                        "header": header.hex(), "target": target.hex(),
                        "seed": seed.hex(), "epoch": 1, "height": HEIGHT, "extranonce": 0})
        else:
            self._send({"error": "not found"})
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n).decode()
        submits[0] += 1
        if submits[0] <= 5:
            print("SUBMIT #%d: %s" % (submits[0], body), flush=True)
        self._send({"result": "accepted", "hash": "00" * 32})

print("mock node on :%d  height=%d  (target: first byte 0x00)" % (PORT, HEIGHT), flush=True)
http.server.ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
