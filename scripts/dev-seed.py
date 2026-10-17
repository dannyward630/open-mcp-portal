#!/usr/bin/env python3
"""Seed a local development install of Open MCP Portal with something to sign in to.

Creates (idempotently, through the management API with the development sign-in):
  - team "demo" (owner admin@localhost, member user@localhost)
  - MCP server "demo-tools" pointing at the fake MCP server (make fake-mcp), published to the catalog
  - team gateway "demo" with every tool and prompt of that server
  - platform gateway "company" with the catalog's tools, granted to the demo team

Needs: make deps-up, make run (DEV_AUTH=true, APP_URL=http://localhost:8088) and make fake-mcp.
Usage: python3 scripts/dev-seed.py [--app http://localhost:8088] [--mcp http://127.0.0.1:8091/mcp]
"""
import argparse
import http.cookiejar
import json
import sys
import urllib.error
import urllib.request


class Session:
    def __init__(self, base: str, account: str):
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.csrf = None
        status, _ = self.call("POST", "/auth/dev", {"account": account})
        if status != 200:
            sys.exit(f"dev sign-in as {account} failed ({status}); is DEV_AUTH on and APP_URL {self.base}?")
        _, me = self.call("GET", "/v1/me")
        self.csrf = me["data"]["csrfToken"]

    def call(self, method, path, body=None, headers=None):
        h = {"Origin": self.base, "Content-Type": "application/json"}
        if self.csrf:
            h["X-CSRF-Token"] = self.csrf
        h.update(headers or {})
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=h)
        try:
            with self.opener.open(req) as r:
                text = r.read().decode()
                return r.status, json.loads(text) if text else None
        except urllib.error.HTTPError as e:
            text = e.read().decode()
            try:
                return e.code, json.loads(text)
            except ValueError:
                return e.code, text

    def must(self, method, path, body=None, headers=None, ok=(200, 201)):
        status, out = self.call(method, path, body, headers)
        if status not in ok:
            sys.exit(f"{method} {path} -> {status}: {out}")
        return out["data"] if isinstance(out, dict) and "data" in out else out


def options(s: Session, path: str):
    items, cursor = [], None
    while True:
        q = f"{path}?limit=500" + (f"&cursor={cursor}" if cursor else "")
        page = s.must("GET", q)
        items += page["items"]
        cursor = page.get("nextCursor")
        if not cursor:
            return items


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--app", default="http://localhost:8088")
    ap.add_argument("--mcp", default="http://127.0.0.1:8091/mcp")
    args = ap.parse_args()
    admin = Session(args.app, "admin")

    # Team with the admin as owner and user@localhost as member.
    teams = admin.must("GET", "/v1/admin/teams?q=demo")["items"]
    if not any(t["team"]["slug"] == "demo" for t in teams):
        admin.must("POST", "/v1/admin/teams", {"slug": "demo", "name": "Demo", "ownerEmail": "admin@localhost"})
        print("created team demo")
    members = admin.must("GET", "/v1/teams/demo/members")
    if not any(m["user"]["email"] == "user@localhost" for m in members):
        admin.must("POST", "/v1/teams/demo/members", {"email": "user@localhost", "role": "member"})

    # MCP server, published.
    servers = admin.must("GET", "/v1/teams/demo/mcp-servers?q=demo-tools")["items"]
    server = next((s for s in servers if s["slug"] == "demo-tools"), None)
    if server is None:
        created = admin.must("POST", "/v1/teams/demo/mcp-servers",
                             {"slug": "demo-tools", "name": "Demo tools", "url": args.mcp,
                              "description": "The fake MCP server from make fake-mcp"})
        server = created["server"]
        if created.get("refreshError"):
            sys.exit(f"discovery failed: {created['refreshError']}; is make fake-mcp running?")
        print(f"registered server demo-tools ({server['counts']})")
    if server["publishState"] in ("private", "rejected"):
        server = admin.must("POST", f"/v1/teams/demo/mcp-servers/{server['id']}/publish", {"note": "dev seed"})
    if server["publishState"] == "pending":
        server = admin.must("POST", f"/v1/admin/mcp-servers/{server['id']}/review", {"decision": "approve"})
        print("published demo-tools to the catalog")

    # Team gateway with every tool and prompt of the server.
    gws = admin.must("GET", "/v1/teams/demo/gateways?q=demo")["items"]
    gw = next((g for g in gws if g["slug"] == "demo"), None)
    if gw is None:
        gw = admin.must("POST", "/v1/teams/demo/gateways", {"slug": "demo", "name": "Demo",
                                                            "description": "Every tool of the demo server"})
        opts = [o for o in options(admin, "/v1/teams/demo/gateway-options") if o["serverId"] == server["id"]]
        admin.must("PUT", f"/v1/teams/demo/gateways/{gw['id']}/capabilities",
                   {"items": [{"capabilityId": o["capabilityId"]} for o in opts]},
                   {"If-Match": f'"{gw["revision"]}"'})
        print(f"created team gateway demo ({len(opts)} capabilities)")

    # Platform gateway with the catalog's tools, granted to the team.
    pgws = admin.must("GET", "/v1/admin/gateways?q=company")["items"]
    pgw = next((g for g in pgws if g["slug"] == "company"), None)
    if pgw is None:
        pgw = admin.must("POST", "/v1/admin/gateways", {"slug": "company", "name": "Company tools",
                                                        "description": "Approved catalog tools"})
        opts = [o for o in options(admin, "/v1/admin/gateway-options") if o["kind"] == "tool"]
        admin.must("PUT", f"/v1/admin/gateways/{pgw['id']}/capabilities",
                   {"items": [{"capabilityId": o["capabilityId"]} for o in opts]},
                   {"If-Match": f'"{pgw["revision"]}"'})
        admin.must("POST", f"/v1/admin/gateways/{pgw['id']}/grants", {"subjectKind": "team", "team": "demo"})
        print(f"created platform gateway company ({len(opts)} tools, granted to demo)")

    print("\nReady. Open", args.app, "and sign in as admin@localhost (or user@localhost).")
    for g in (gw, pgw):
        print(f"  {g['name']}: {g['url']}")


if __name__ == "__main__":
    main()
