# Verifying the gateway's identity token

When a person calls a tool, gets a prompt or reads a resource through a gateway, the gateway forwards the request to your MCP server with a short-lived **identity token**: a JWT signed by the gateway that says who is calling, through which gateway and for which tool ([decisions](decisions.md) 7 and 19). Verify it before you trust it. The token is the only credential from the caller that reaches your server. The gateway never forwards the client's own API key or OAuth token.

Lists (`tools/list`, `prompts/list`, `resources/list`, `resources/templates/list`) are served from the definitions approved in the gateway, so your server only receives calls (`tools/call`, `prompts/get`, `resources/read`). A read arrives with your own URI (the gateway strips its `omcp://{server-slug}/` prefix). Each call also performs the MCP handshake again, because the gateway is stateless.

## Where the token arrives

| Your server's static header (set when it was registered) | The token arrives as |
|---|---|
| none, or any header other than `Authorization` (for example `X-API-Key`) | `Authorization: Bearer <jwt>` |
| `Authorization` | `MCP-Gateway-Identity: <jwt>` (the bare token). Your static `Authorization` value is sent unchanged |

With no static header, your server can act as an ordinary OAuth resource server that accepts bearer tokens from the gateway's JWKS. Servers can't register a static header named `MCP-Gateway-Identity`.

## What to check

| Item | Value |
|---|---|
| Algorithm | `ES256` only (header `alg`), and the header `kid` names the key |
| Keys | `GET <gateway>/.well-known/jwks.json`, a JWK Set of the key that signs now, the next key (published at least a day before it signs, `SIGNING_KEY_PREPUBLISH`) and any key retired in the last day. Cache it for up to 5 minutes (`Cache-Control: max-age=300`). Keys rotate every 30 days; a cached set normally already holds the next key, but still fetch the set again when a token has a `kid` you haven't seen (the very first key signs as soon as it exists, and so does a new key if a gateway ever finds no key to sign with) |
| `iss` | The gateway's URL (its `APP_URL`, for example `https://mcp.example.com`) |
| `aud` | **Your server's URL exactly as registered** (for example `https://status.example.com/mcp`). A token minted for another server must be rejected |
| `exp`, `nbf`, `iat` | `exp` is 5 minutes after `iat`, and `nbf` is 30 seconds before it. Allow a little clock skew |

The claims:

| Claim | Meaning |
|---|---|
| `sub` | The person's user ID in the gateway (a UUID, stable) |
| `email`, `name` | Their email and display name at the time of the call |
| `teams` | Slugs of the teams they belong to |
| `gateway` | The gateway's slug |
| `tool` / `prompt` | The exposed name of the tool (`tools/call`) or prompt (`prompts/get`), `{server-slug}__{name}` |
| `resource` | The exposed URI read (`resources/read`): `omcp://{server-slug}/{your-uri}`, for example `omcp://status/status://services/email`. Exactly one of `tool`, `prompt` and `resource` is set |
| `client` | The MCP client's `clientInfo.name` when the request carried it, otherwise the name of the person's API key |
| `jti` | A unique ID for the token (useful in your logs) |

The gateway has already checked that the person may use this tool. Your server decides what they may do with it, for example by reading `sub`, `email` or `teams`.

## Go (go-jose)

```go
import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const gatewayURL = "https://mcp.example.com"
const myURL = "https://status.example.com/mcp" // as registered in the gateway

type Identity struct {
	Email   string   `json:"email"`
	Name    string   `json:"name"`
	Teams   []string `json:"teams"`
	Gateway string   `json:"gateway"`
	Tool    string   `json:"tool"`
}

// Fetch the JWKS (cache it for up to 5 minutes, and fetch again for an
// unknown kid).
func fetchKeys() (jose.JSONWebKeySet, error) {
	var set jose.JSONWebKeySet
	res, err := http.Get(gatewayURL + "/.well-known/jwks.json")
	if err != nil {
		return set, err
	}
	defer res.Body.Close()
	return set, json.NewDecoder(res.Body).Decode(&set)
}

func verify(r *http.Request, keys jose.JSONWebKeySet) (jwt.Claims, Identity, error) {
	raw := r.Header.Get("MCP-Gateway-Identity")
	if raw == "" {
		raw = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	var std jwt.Claims
	var id Identity
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(tok.Headers) != 1 {
		return std, id, err
	}
	matches := keys.Key(tok.Headers[0].KeyID)
	if len(matches) == 0 {
		return std, id, errUnknownKey // fetch the JWKS again and retry once
	}
	if err := tok.Claims(matches[0].Key, &std, &id); err != nil {
		return std, id, err
	}
	err = std.ValidateWithLeeway(jwt.Expected{Issuer: gatewayURL, AnyAudience: jwt.Audience{myURL}, Time: time.Now()}, 30*time.Second)
	return std, id, err // std.Subject is the person's user ID
}
```

`cmd/fakemcp` has a working version of this check (`testutil.VerifyIdentity`), and its `whoami` tool reports the token it received.

## Python (PyJWT)

```python
import jwt  # pip install "pyjwt[crypto]"

GATEWAY = "https://mcp.example.com"
MY_URL = "https://status.example.com/mcp"  # as registered in the gateway

# Caches keys; fetches the set again when it meets an unknown kid.
jwks = jwt.PyJWKClient(f"{GATEWAY}/.well-known/jwks.json", cache_jwk_set=True, lifespan=300)

def identity(headers) -> dict:
    token = headers.get("MCP-Gateway-Identity") or headers.get("Authorization", "").removeprefix("Bearer ")
    key = jwks.get_signing_key_from_jwt(token)
    return jwt.decode(
        token, key.key, algorithms=["ES256"], issuer=GATEWAY, audience=MY_URL, leeway=30,
        options={"require": ["exp", "iat", "iss", "aud", "sub"]},
    )  # claims["sub"], claims["email"], claims["teams"], claims["tool"]
```

## TypeScript (jose)

```ts
import { createRemoteJWKSet, jwtVerify } from "jose";

const GATEWAY = "https://mcp.example.com";
const MY_URL = "https://status.example.com/mcp"; // as registered in the gateway

// Caches keys and fetches the set again for an unknown kid.
const jwks = createRemoteJWKSet(new URL(`${GATEWAY}/.well-known/jwks.json`), { cacheMaxAge: 300_000 });

export async function identity(headers: Headers) {
  const token = headers.get("mcp-gateway-identity") ?? headers.get("authorization")?.replace(/^Bearer /, "") ?? "";
  const { payload } = await jwtVerify(token, jwks, {
    algorithms: ["ES256"], issuer: GATEWAY, audience: MY_URL, clockTolerance: 30,
  });
  return payload; // payload.sub, payload.email, payload.teams, payload.tool
}
```

## Try it locally

```sh
make fake-mcp   # go run ./cmd/fakemcp -jwks http://localhost:8088/.well-known/jwks.json
```

Register `http://127.0.0.1:8091/mcp` in a team, select its `whoami` tool in a gateway, create an API key under Account › API keys, and call it:

```sh
curl -s http://localhost:8088/mcp/<gateway> -H "Authorization: Bearer omcp_..." \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"<server>__whoami","arguments":{}}}'
```

The result shows the header the token arrived in, its claims and `"verified": true` (with `-jwks`; without it, the claims are only decoded). The fake server also logs each call's identity.
