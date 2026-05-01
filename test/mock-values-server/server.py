from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs
import json

ALLOWED_VALUES = {
    "system:serviceaccount:default:deploy-bot": ["db-001", "db-002", "db-003"],
    "alice": ["db-001"],
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        params = parse_qs(urlparse(self.path).query)
        user = params.get("user", [""])[0]
        values = ALLOWED_VALUES.get(user, [])
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(values).encode())

    def log_message(self, format, *args):
        pass


if __name__ == "__main__":
    server = HTTPServer(("0.0.0.0", 8090), Handler)
    print("Mock values server listening on :8090", flush=True)
    server.serve_forever()
