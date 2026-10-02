#!/usr/bin/env python3
"""Preview the Jellyfin plugin page without Jellyfin.

Serves jellyfin-plugin/Web/dvbhub.html with a stand-in for Jellyfin's
ApiClient, and forwards /DvbHub/Api/* and /DvbHub/Ui/* to a running dvbhub,
like the plugin's C# controller does.

    python3 tools/plugin-preview.py [dvbhub-url] [port]
    # then open http://127.0.0.1:8097/
"""
import http.server
import json
import os
import sys
import urllib.error
import urllib.request

DVBHUB = (sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:9980").rstrip("/")
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 8097
HERE = os.path.dirname(os.path.abspath(__file__))
PAGE = os.path.join(HERE, "..", "jellyfin-plugin", "Web", "dvbhub.html")
LIVETV = {"TunerHosts": [], "ListingProviders": []}
CONFIG = {"DvbHubUrl": DVBHUB, "AdminPassword": ""}

SHELL = """<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>dvbhub plugin preview</title>
<style>body{margin:0;background:#101010;color:#ddd;font:15px/1.4 "Noto Sans",sans-serif}
.skinHeader{padding:12px 16px;background:#1c1c1c;color:#00a4dc;font-weight:700}</style></head>
<body><div class="skinHeader">Jellyfin dashboard (preview)</div>
<script>
window.ApiClient = {
  accessToken: function () { return 'preview'; },
  getUrl: function (name, params) {
    var q = params ? '?' + Object.keys(params).map(function (k) { return k + '=' + encodeURIComponent(params[k]); }).join('&') : '';
    return '/' + name + q;
  },
  getPluginConfiguration: function () { return fetch('/_config').then(function (r) { return r.json(); }); },
  updatePluginConfiguration: function (id, c) { return fetch('/_config', { method: 'POST', body: JSON.stringify(c) }); },
  getNamedConfiguration: function () { return fetch('/_livetv').then(function (r) { return r.json(); }); },
  ajax: function (o) { return fetch(o.url, { method: o.type, body: o.data, headers: { 'Content-Type': o.contentType } }); }
};
window.Dashboard = { alert: function (m) { alert(m); } };
</script>
%PAGE%
<script>document.querySelector('#dvbhubPage').dispatchEvent(new Event('pageshow'));</script>
</body></html>"""


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def send(self, code, body, ctype="application/json"):
        data = body if isinstance(body, bytes) else body.encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else None

    def forward(self, path):
        req = urllib.request.Request(CONFIG["DvbHubUrl"].rstrip("/") + path, data=self.body(), method=self.command)
        if self.headers.get("Content-Type"):
            req.add_header("Content-Type", self.headers["Content-Type"])
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                self.send(r.status, r.read(), r.headers.get("Content-Type", "application/json"))
        except urllib.error.HTTPError as e:
            self.send(e.code, e.read(), e.headers.get("Content-Type", "application/json"))
        except Exception as e:  # noqa: BLE001 - shown to the page like the plugin does
            self.send(502, json.dumps({"error": "can't reach dvbhub: %s" % e}))

    def route(self):
        p = self.path
        if p == "/" or p.startswith("/?"):
            with open(PAGE, encoding="utf-8") as f:
                return self.send(200, SHELL.replace("%PAGE%", f.read()), "text/html; charset=utf-8")
        if p == "/_config":
            if self.command == "POST":
                CONFIG.update(json.loads(self.body() or b"{}"))
            return self.send(200, json.dumps(CONFIG))
        if p == "/_livetv":
            return self.send(200, json.dumps(LIVETV))
        if p.startswith("/LiveTv/TunerHosts"):
            LIVETV["TunerHosts"].append(json.loads(self.body() or b"{}"))
            return self.send(200, "{}")
        if p.startswith("/LiveTv/ListingProviders"):
            LIVETV["ListingProviders"].append(json.loads(self.body() or b"{}"))
            return self.send(200, "{}")
        if p.startswith("/DvbHub/Api/"):
            return self.forward("/api/" + p[len("/DvbHub/Api/"):])
        if p in ("/DvbHub/Ui/app.js", "/DvbHub/Ui/app.css"):
            return self.forward("/" + p.rsplit("/", 1)[1])
        self.send(404, json.dumps({"error": "not found"}))

    do_GET = do_POST = do_PUT = do_DELETE = route


if __name__ == "__main__":
    print("plugin preview on http://127.0.0.1:%d/ -> %s" % (PORT, DVBHUB))
    http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
