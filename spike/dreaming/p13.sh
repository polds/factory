#!/usr/bin/env bash
# P13 probe. Runs the harness binary (the process the task image starts)
# against a local fake model. The tool reads only this process's parent
# environ. Writes {"reexec":true,"hits":N} for the preflight job file.
set -euo pipefail

out="${1:?usage: spike/dreaming/p13.sh <probe.json>}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
go build -o "${work}/factory-harness" ./cmd/factory-harness

python3 - "${work}" "${out}" <<'PY'
import json, os, socket, subprocess, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

work, out = sys.argv[1], sys.argv[2]
needle = "p13-sentinel-value"
hits_path = os.path.join(work, "hits.txt")
seen = {"n": 0}

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def do_POST(self):
        seen["n"] += 1
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("x-litellm-response-cost", "0.01")
        self.end_headers()
        if seen["n"] == 1:
            command = (
                "hits=0\n"
                "self=$$\n"
                "parent=$(awk '/^PPid:/ { print $2 }' /proc/$self/status)\n"
                "if [ -r /proc/$parent/environ ] && tr '\\0' '\\n' < /proc/$parent/environ | grep -F -q -- '%s'; then hits=$((hits+1)); fi\n"
                "printf '%%s\\n' \"$hits\" > %s\n"
            ) % (needle, hits_path)
            body = {
                "model": "served",
                "choices": [{"message": {"role": "assistant", "tool_calls": [{
                    "id": "c1", "type": "function",
                    "function": {"name": "bash", "arguments": json.dumps({"command": command})},
                }]}}],
            }
        else:
            body = {"model": "served", "choices": [{"message": {"role": "assistant", "content": "done"}}]}
        self.wfile.write(json.dumps(body).encode())

sock = socket.socket()
sock.bind(("127.0.0.1", 0))
port = sock.getsockname()[1]
sock.close()
server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
env = os.environ.copy()
env.update({
    "OPENAI_BASE_URL": "http://127.0.0.1:%d" % port,
    "OPENAI_API_KEY": needle,
    "FACTORY_TASK_PROMPT": "scan",
    "FACTORY_MAX_TURNS": "2",
    "FACTORY_HARNESS_KEEP_DUMPABLE": "1",
    "FACTORY_RUNTIME_DIR": work,
})
proc = subprocess.run(
    [os.path.join(work, "factory-harness"), "--task-id", "p13", "--model", "m"],
    cwd=work, env=env, capture_output=True, text=True, timeout=60,
)
server.shutdown()
hits = 0
if os.path.exists(hits_path):
    hits = int(open(hits_path, encoding="utf-8").read().strip() or "0")
with open(out, "w", encoding="utf-8") as handle:
    json.dump({"reexec": True, "hits": hits}, handle)
    handle.write("\n")
if proc.returncode != 0 and not os.path.exists(hits_path):
    raise SystemExit(proc.stderr[-400:])
PY
