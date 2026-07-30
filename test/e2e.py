import json, base64, hashlib, os, re, secrets, urllib.request, urllib.parse, urllib.error, sys

BASE="http://127.0.0.1:2020"
def req(path, data=None, headers=None, method=None, raw=False, allow_redirect=True):
    url = path if path.startswith("http") else BASE+path
    h = {"Content-Type":"application/json"}
    if headers: h.update(headers)
    body=None
    if data is not None:
        body = data if isinstance(data,bytes) else json.dumps(data).encode()
    r = urllib.request.Request(url, data=body, headers=h, method=method)
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self,*a,**k): return None
    op = urllib.request.build_opener() if allow_redirect else urllib.request.build_opener(NoRedirect)
    try:
        resp = op.open(r, timeout=15)
        return resp.status, {k.lower():v for k,v in resp.headers.items()}, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, {k.lower():v for k,v in e.headers.items()}, e.read()

ok=lambda c,m: print(("PASS" if c else "FAIL")+" "+m) or (c or sys.exit("FAILED: "+m))

s,h,b = req("/healthz")
ok(s==200 and json.loads(b)["status"]=="ok", "healthz")

s,h,b = req("/mcp", data={"jsonrpc":"2.0","id":1,"method":"initialize"})
ok(s==401 and "resource_metadata" in h.get("www-authenticate",""), "unauthenticated /mcp challenges with resource metadata")

s,h,b = req("/.well-known/oauth-authorization-server")
meta=json.loads(b); ok(s==200 and meta["issuer"]==BASE.replace("127.0.0.1","127.0.0.1"), "authorization server metadata")

s,h,b = req("/register", data={"client_name":"e2e","redirect_uris":["http://localhost:9999/cb"]})
reg=json.loads(b); ok(s in (200,201) and reg.get("client_id"), "dynamic client registration")
cid=reg["client_id"]

verifier = base64.urlsafe_b64encode(secrets.token_bytes(48)).decode().rstrip("=")
challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
q = urllib.parse.urlencode({"response_type":"code","client_id":cid,"redirect_uri":"http://localhost:9999/cb",
    "state":"xyz","code_challenge":challenge,"code_challenge_method":"S256"})
s,h,b = req("/authorize?"+q)
ok(s==200 and b'name="csrf"' in b, "login form rendered")
csrf = re.search(rb'name="csrf" value="([^"]+)"', b).group(1).decode()

form = urllib.parse.urlencode({"csrf":csrf,"client_id":cid,"redirect_uri":"http://localhost:9999/cb",
    "username":"andreas","password":"wrong"}).encode()
s,h,b = req("/authorize", data=form, headers={"Content-Type":"application/x-www-form-urlencoded"}, allow_redirect=False)
ok(s==401 or b"Invalid" in b or b"incorrect" in b.lower(), "wrong password refused")

s,h,b = req("/authorize?"+q)
csrf = re.search(rb'name="csrf" value="([^"]+)"', b).group(1).decode()
form = urllib.parse.urlencode({"csrf":csrf,"client_id":cid,"redirect_uri":"http://localhost:9999/cb",
    "username":"andreas","password":"supersecret123"}).encode()
s,h,b = req("/authorize", data=form, headers={"Content-Type":"application/x-www-form-urlencoded"}, allow_redirect=False)
loc = h.get("location","")
ok(s in (302,303) and "code=" in loc, "login redirects with an authorization code")
code = urllib.parse.parse_qs(urllib.parse.urlparse(loc).query)["code"][0]

form = urllib.parse.urlencode({"grant_type":"authorization_code","code":code,"client_id":cid,
    "redirect_uri":"http://localhost:9999/cb","code_verifier":verifier}).encode()
s,h,b = req("/token", data=form, headers={"Content-Type":"application/x-www-form-urlencoded"})
tok=json.loads(b); ok(s==200 and tok.get("access_token"), "token exchange with PKCE")
AT=tok["access_token"]

form = urllib.parse.urlencode({"grant_type":"authorization_code","code":code,"client_id":cid,
    "redirect_uri":"http://localhost:9999/cb","code_verifier":verifier}).encode()
s,h,b = req("/token", data=form, headers={"Content-Type":"application/x-www-form-urlencoded"})
ok(s>=400, "an authorization code cannot be replayed")

AUTH={"Authorization":"Bearer "+AT}
SID={}
def rpc(method, params=None, id=1):
    d={"jsonrpc":"2.0","id":id,"method":method}
    if params is not None: d["params"]=params
    hh=dict(AUTH); hh.update(SID)
    s,h,b = req("/mcp", data=d, headers=hh)
    if h.get("mcp-session-id"): SID["Mcp-Session-Id"]=h["mcp-session-id"]
    return s, json.loads(b)

s,r = rpc("initialize")
ok(s==200 and r["result"]["serverInfo"]["name"]=="secondbrain", "initialize")
ok("wiki" in r["result"]["instructions"], "vault instructions travel in the initialize response")

s,r = rpc("tools/list")
tools=[t["name"] for t in r["result"]["tools"]]
ok(len(tools)>=30, f"tools/list returned {len(tools)} tools")
for must in ["note_search","note_read","note_section_edit","note_move","vault_grep","note_related","daily_note","task_list","note_history"]:
    ok(must in tools, f"tool {must} present")

def call(name, args):
    s,r = rpc("tools/call", {"name":name,"arguments":args})
    res = r["result"]
    if res.get("isError"): return None, res["content"][0]["text"]
    return json.loads(res["content"][0]["text"]), None

d,e = call("vault_list", {})
ok(e is None and d["vaults"][0]["name"]=="default", "vault_list")

d,e = call("note_create", {"path":"wiki/mcp.md","title":"MCP","tags":["protocol"],
    "content":"# MCP\n\n## Idea\n\nA protocol for tools.\n\n## Related\n"})
ok(e is None and d["created"], "note_create over MCP")

d,e = call("note_create", {"path":"wiki/mcp.md"})
ok(e is not None and "already exists" in e, "duplicate note refused with a useful message")

d,e = call("note_search", {"query":"protocol for tools"})
ok(e is None and d["total"]==1, "note_search finds it")

d,e = call("note_read", {"path":"wiki/mcp"})
h = d["content_hash"]; ok(e is None and h, "note_read returns a content hash")

d,e = call("note_section_edit", {"path":"wiki/mcp.md","mode":"append_to_section",
    "heading":"Idea","content":"It is transport agnostic.","content_hash":h})
ok(e is None and "diff" in d, "note_section_edit returns a diff")

d,e = call("note_read", {"path":"wiki/mcp.md"})
ok("transport agnostic" in d["content"] and "## Related" in d["content"], "the rest of the note survived")

d,e = call("note_write", {"path":"wiki/mcp.md","content":"x","content_hash":"0"*32})
ok(e is not None and "changed since" in e, "stale write refused")

d,e = call("daily_note", {"append":"Wrote the e2e test."})
ok(e is None and d["path"].startswith("journal/"), "daily_note")

d,e = call("inbox_capture", {"text":"Remember to document the trash retention."})
ok(e is None and d["path"].startswith("inbox/"), "inbox_capture")

d,e = call("note_history", {"path":"wiki/mcp.md"})
ok(e is None and len(d["revisions"])>=2, f"note_history has {len(d['revisions']) if d else 0} revisions")

d,e = call("note_diff", {"path":"wiki/mcp.md"})
ok(e is None and "transport agnostic" in d["diff"], "note_diff shows the change")

d,e = call("vault_stats", {})
ok(e is None and d["stats"]["notes"]>=3, "vault_stats")

d,e = call("vault_create", {"name":"work","layout":"zettelkasten"})
ok(e is None and "zettel/" in d["directories"], "vault_create with a chosen layout")

d,e = call("note_create", {"vault":"work","path":"zettel/a.md","content":"# A\n"})
ok(e is None, "writing into the second vault")

d,e = call("note_read", {"vault":"nope","path":"x"})
ok(e is not None and "unknown vault" in e, "unknown vault refused")

d,e = call("note_read", {"path":"../../../etc/passwd"})
ok(e is not None, "path traversal refused")

d,e = call("note_read", {"path":".secondbrain/index.db"})
ok(e is not None, "internal directory unreachable")

s,h,b = req("/mcp", method="DELETE", headers=dict(list(AUTH.items())+list(SID.items())))
ok(s==204, "session delete")

print("\nALL E2E CHECKS PASSED")
