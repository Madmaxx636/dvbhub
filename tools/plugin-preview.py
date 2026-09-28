#!/usr/bin/env python3
"""Preview the Jellyfin plugin page without Jellyfin.

Serves jellyfin-plugin/Web/dvbhub.html with stand-ins for Jellyfin's ApiClient
and Dashboard objects, and forwards /api/* to a running dvbhub (default
http://127.0.0.1:9980) the way the plugin's server-side proxy does.

    python3 tools/plugin-preview.py [--port 9981] [--dvbhub http://127.0.0.1:9980]
"""
import argparse
import http.server
import pathlib
import re
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
PAGE = ROOT / "jellyfin-plugin" / "Web" / "dvbhub.html"

STUBS = """
window.ApiClient = {
  accessToken: () => 'preview',
  getUrl: (name, params) => '/' + name.replace(/^DvbHub\\/Api\\//, 'api/') + (params ? '?' + new URLSearchParams(params) : ''),
  getPluginConfiguration: () => Promise.resolve({DvbHubUrl: '%(dvbhub)s', AdminPassword: ''}),
  updatePluginConfiguration: () => Promise.resolve({}),
  getNamedConfiguration: () => Promise.resolve({TunerHosts: [], ListingProviders: []}),
  ajax: (o) => { console.log('Jellyfin API call (preview only):', o.type, o.url, o.data); return Promise.resolve(); },
};
window.Dashboard = {
  alert: (m) => { const t = document.getElementById('previewToast'); t.textContent = m; t.style.display = 'block'; setTimeout(() => t.style.display = 'none', 3000); },
  showLoadingMsg() {}, hideLoadingMsg() {}, processPluginConfigurationUpdateResult() {},
};
"""


def harness(dvbhub):
    html = PAGE.read_text()
    m = re.search(r'<script type="text/javascript">(.*?)</script>', html, re.S)
    markup = html[: m.start()] + html[m.end():]
    return f"""<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>dvbhub plugin preview</title>
<style>body{{margin:0;background:#101010;color:#ddd;font:15px/1.4 "Noto Sans",system-ui,sans-serif}} .content-primary{{max-width:1200px;margin:0 auto;padding:16px}}
#previewBanner{{background:#00a4dc;color:#fff;padding:6px 16px;font-size:13px}}
#previewToast{{display:none;position:fixed;bottom:16px;right:16px;background:#333;color:#fff;padding:10px 14px;border-radius:8px}}</style>
</head><body><div id="previewBanner">Preview of the Jellyfin plugin page (Jellyfin dashboard styles not included)</div>
{markup}<div id="previewToast"></div>
<script>{STUBS % {'dvbhub': dvbhub}}</script>
<script>{m.group(1)}</script>
<script>document.querySelector('#dvbhubPage').dispatchEvent(new Event('pageshow'));</script>
</body></html>"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9981)
    ap.add_argument("--dvbhub", default="http://127.0.0.1:9980")
    args = ap.parse_args()

    class Handler(http.server.BaseHTTPRequestHandler):
        def _proxy(self):
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length) if length else None
            req = urllib.request.Request(args.dvbhub + self.path, data=body, method=self.command)
            if body is not None:
                req.add_header("Content-Type", self.headers.get("Content-Type", "application/json"))
            try:
                with urllib.request.urlopen(req, timeout=30) as r:
                    code, ctype, data = r.status, r.headers.get("Content-Type", "application/json"), r.read()
            except urllib.error.HTTPError as e:
                code, ctype, data = e.code, e.headers.get("Content-Type", "application/json"), e.read()
            except OSError as e:
                code, ctype, data = 502, "application/json", ('{"error":"cannot reach dvbhub: %s"}' % e).encode()
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _handle(self):
            if self.path.startswith("/api/"):
                return self._proxy()
            data = harness(args.dvbhub).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        do_GET = do_POST = do_PUT = do_DELETE = _handle

        def log_message(self, *a):
            pass

    print(f"plugin preview on http://127.0.0.1:{args.port}/ -> dvbhub {args.dvbhub}")
    http.server.ThreadingHTTPServer(("127.0.0.1", args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
