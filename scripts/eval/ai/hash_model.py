# A stand-in model server that knows nothing: its "risk" is a hash of the
# prompt text, so it can't be related to what happened after merge.
import json, hashlib, sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        h = int(hashlib.sha256(body['messages'][1]['content'].encode()).hexdigest(), 16)
        out = json.dumps({"model": "hash-scorer", "choices": [{"message": {"content": json.dumps({"risk": h % 101})}}]}).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a): pass
ThreadingHTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
