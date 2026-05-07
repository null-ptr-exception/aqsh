from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs
import json

ALLOWED_VALUES = {
    "system:serviceaccount:default:deploy-bot": ["db-001", "db-002", "db-003"],
    "alice": ["db-001"],
}

INSTANCES_BY_REGION = {
    "us-east-1": ["db-us-001", "db-us-002"],
    "eu-west-1": ["db-eu-001"],
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        params = parse_qs(parsed.query)
        path = parsed.path

        if path == "/instances-by-region":
            region = params.get("region", [""])[0]
            values = INSTANCES_BY_REGION.get(region, [])
        else:
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
