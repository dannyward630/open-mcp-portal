# Deploying Open MCP Gateway

Open MCP Gateway is one stateless binary in one container image. It needs
PostgreSQL, an OIDC provider and TLS in front of it. There is no worker, cache,
queue or object storage.

| Path | What |
|---|---|
| [`Dockerfile`](../Dockerfile) | Image: UI built with `npm ci`, Go built with `-trimpath` and version ldflags, distroless `nonroot` runtime. Default target is the gateway; `--target fakemcp` is the demo's fake MCP server. |
| [`compose.demo.yaml`](../compose.demo.yaml) | Local demo: gateway, Postgres and the fake MCP server, with development sign-in. |
| [`deploy/kubernetes/base`](../deploy/kubernetes/base) | Kustomize base: Deployment (+ migrate init container), Service, PodDisruptionBudget, NetworkPolicies, ServiceAccount and a generated ConfigMap. |
| [`deploy/kubernetes/overlays/example`](../deploy/kubernetes/overlays/example) | Example overlay: namespace, pinned image, settings, NGINX Ingress (or Gateway API `httproute.yaml`), narrowed NetworkPolicies. |

## Image

```sh
make docker                     # open-mcp-gateway:<git describe>
docker run --rm open-mcp-gateway:<tag> version
```

- Commands: `serve` (default), `migrate`, `doctor`, `version`. The binary is `/open-mcp-gateway`. The image has no shell.
- The image runs as UID/GID 65532, listens on 8088 (`LISTEN_ADDR`, default `:8088`) and needs no writable filesystem.
- There is no `HEALTHCHECK` because distroless has no shell or curl. Orchestrators probe `/healthz` and `/readyz`, as in [Health checks](#health-checks).
- Multi-arch: `docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=… --build-arg COMMIT=… .`.
  The UI and Go builds run on the build platform and cross-compile.

## Demo

```sh
make demo        # docker compose -f compose.demo.yaml up --build
```

Open <http://localhost:8090> and sign in as `admin`. Register the fake MCP server in a team as `http://127.0.0.1:8091/mcp`
(it shares the gateway's network namespace, so the address is loopback to the gateway), build a gateway and call it at
`http://localhost:8090/mcp/<slug>` with a personal API key. `OMG_DEMO_PORT` changes the port. `make demo-down` deletes the
data.

**Development only.** The demo uses development sign-in and the public example keys from `.env.example`. The gateway
accepts them only because `APP_URL` is loopback.

## Required settings and secrets

Every setting is described in [`.env.example`](../.env.example). They come from environment variables or from a flat YAML
file named by `OMG_CONFIG_FILE`. Each secret can also be read from a file named by `<NAME>_FILE`, for example
`DATABASE_URL_FILE=/run/secrets/database-url`.

| Setting | Required | Notes |
|---|---|---|
| `APP_URL` | yes | Public **https** origin, for example `https://mcp.example.org`. It must match the Ingress host. |
| `DATABASE_URL` | yes (secret) | `postgres://user:pass@host:5432/db?sslmode=require`. URL-encode the password. |
| `ENCRYPTION_KEY` | yes (secret) | 32 random bytes, base64. Encrypts MCP server header values and the identity signing keys. |
| `ENCRYPTION_KEY_PREVIOUS` | while rotating (secret) | The old key, still accepted for decryption. |
| `API_KEY_PEPPER` | yes (secret) | 32 random bytes, base64. Keys the HMAC that stores personal API keys. **Changing it invalidates every API key**, and it has no `_PREVIOUS`. |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID` | yes | The only production sign-in method. The redirect URI to register is `${APP_URL}/auth/callback`. |
| `OIDC_CLIENT_SECRET` | yes (secret) | |
| `BOOTSTRAP_ADMIN_SUBJECT` | first install | The OIDC `sub` made platform admin on first sign-in. It takes effect once. |
| `TRUSTED_PROXIES` | recommended | Ingress controller CIDRs allowed to set `X-Forwarded-For`, used for logging and sign-in rate limits. |
| `OIDC_ALLOWED_EMAIL_DOMAINS`, `OIDC_GROUPS_CLAIM`, `INSTANCE_NAME`, `ORG_NAME`, `SUPPORT_URL`, … | optional | |

Generate keys. `ENCRYPTION_KEY`, `ENCRYPTION_KEY_PREVIOUS` and `API_KEY_PEPPER` must each decode to exactly 32 bytes:

```sh
openssl rand -base64 32     # ENCRYPTION_KEY
openssl rand -base64 32     # API_KEY_PEPPER (a different value)
```

**Safety checks.** Unless `APP_URL` is a loopback address, `serve` refuses to start in any of these cases:

- `ENCRYPTION_KEY` or `API_KEY_PEPPER` is the public example value from `.env.example`.
- `DEV_AUTH=true`.
- `APP_URL` is `http://`.

`serve` also requires a sign-in method. Without `DEV_AUTH`, that means OIDC. OIDC discovery runs on the first sign-in,
not at startup, so a misconfigured issuer shows up at sign-in and in the logs. `migrate` needs only `DATABASE_URL`, and it
applies the safety checks only when `APP_URL` is set. `open-mcp-gateway doctor` prints all of this and also checks
Postgres and the migration state:

```sh
kubectl -n open-mcp-gateway exec deploy/open-mcp-gateway -c gateway -- /open-mcp-gateway doctor
```

## Kubernetes

### Secrets

The base references the secret `open-mcp-gateway-runtime` by name and never contains values. Create it with your
secrets operator (an `ExternalSecret`, for example) or by hand:

```sh
kubectl create namespace open-mcp-gateway   # or apply the overlay's namespace.yaml first
kubectl -n open-mcp-gateway create secret generic open-mcp-gateway-runtime \
  --from-literal=DATABASE_URL='postgres://omg:<password>@db.example.org:5432/omg?sslmode=require' \
  --from-literal=ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=API_KEY_PEPPER="$(openssl rand -base64 32)" \
  --from-literal=OIDC_CLIENT_SECRET='<from your IdP>'
```

| Key | Used by |
|---|---|
| `DATABASE_URL` | `migrate` init container and `gateway` |
| `ENCRYPTION_KEY`, `API_KEY_PEPPER`, `OIDC_CLIENT_SECRET` | `gateway` (required: a missing key stops the pod with `CreateContainerConfigError`) |
| `ENCRYPTION_KEY_PREVIOUS` | `gateway`, optional, only during a rotation |

Store `ENCRYPTION_KEY` and `API_KEY_PEPPER` in your secrets store as well. A database backup is useless without them:
see [Backups](#backups). Secrets are not hashed into the pod template, so after changing one, run
`kubectl -n open-mcp-gateway rollout restart deploy/open-mcp-gateway` (or use your operator's reloader).

### Configuration

`open-mcp-gateway-config` is generated from [`base/config.env`](../deploy/kubernetes/base/config.env), so its name carries
a content hash and **a configuration change rolls the pods**. Merge keys in an overlay:

```yaml
configMapGenerator:
  - name: open-mcp-gateway-config
    behavior: merge
    literals:
      - APP_URL=https://mcp.example.org
      - OIDC_ISSUER=https://idp.example.org
```

The base sets `LISTEN_ADDR=:8088`, `LOG_FORMAT=json`, `DEV_AUTH=false`, `MIGRATE_ON_START=false` (the Deployment also
pins it), `SHUTDOWN_DELAY=10s`, `SHUTDOWN_TIMEOUT=125s`, and placeholders for `APP_URL`, `OIDC_ISSUER` and
`OIDC_CLIENT_ID`. Do not set `generatorOptions.disableNameSuffixHash`, because then pods keep running with stale settings.

### What the base deploys

- **Deployment `open-mcp-gateway`**: 2 replicas, `maxUnavailable: 0` / `maxSurge: 1`, spread across nodes (and, best
  effort, zones), with a `migrate` init container. The pod runs as non-root UID 65532 with a read-only root filesystem, all
  capabilities dropped, no privilege escalation, seccomp `RuntimeDefault`, an `emptyDir` on `/tmp`, no service-account
  token and no service links. That meets the `restricted` Pod Security Standard, and the example namespace enforces it.
  Resources: requests 100m CPU / 128Mi, limits 1 CPU / 512Mi, with `GOMEMLIMIT` set from the memory limit.
- **Service `open-mcp-gateway`**: port `http` 80 → 8088. No session affinity.
- **PodDisruptionBudget**: `maxUnavailable: 1`. Unhealthy pods never block a drain.
- **ServiceAccount** with `automountServiceAccountToken: false`. The gateway never calls the Kubernetes API.
- **NetworkPolicies** select only the gateway pods, so the base can share a namespace:
  - default deny;
  - ingress on TCP 8088 from the ingress controller's namespace (placeholder `ingress-controller`; patch it);
  - DNS to kube-dns in `kube-system`;
  - TCP 5432 to anywhere (narrow it to your database);
  - TCP 443 to anywhere, for upstream MCP servers and the OIDC provider.

  **Upstream MCP servers on another port** (for example `https://mcp.example.com:8443`) need an extra egress rule in the
  overlay, as the example shows. The gateway's SSRF guard applies whatever the policy allows. Outside development it
  connects only to **public** addresses and never through an HTTP(S) proxy. MCP servers on cluster-internal or private
  addresses cannot be registered yet.

### Ingress

- The host must equal `APP_URL`'s host, and TLS terminates at the ingress.
- **Timeouts.** A `/mcp/{slug}` call waits up to 120s for its upstream server (the maximum per-server timeout). Allow
  reads of a few minutes: the example sets `proxy-read-timeout: 300` for ingress-nginx and `timeouts.request: 300s` on the
  HTTPRoute. Responses are buffered JSON today, but SSE relays are planned, so the example already turns off proxy
  buffering. Requests are capped at 1 MiB.
- **No sticky sessions.** `/mcp/{slug}` is stateless: no MCP session lives in a pod, and browser sessions and OIDC login
  state are in PostgreSQL. Any replica can serve any request.
- Set `TRUSTED_PROXIES` to the controller's pod network.

### Health checks

| Endpoint | Meaning | Probe |
|---|---|---|
| `/healthz` | The process is up. It never checks dependencies, so a database outage does not restart pods. | liveness, startup |
| `/readyz` | Postgres answers within 2s and the pod is not draining; otherwise 503 with the failing check. | readiness |

The listener opens only after Postgres answers (the process waits up to 3 minutes) and the schema is current. The startup
probe allows 4 minutes.

**Graceful shutdown.** On SIGTERM the gateway fails `/readyz` and waits `SHUTDOWN_DELAY` (10s) so endpoints and load
balancers stop sending traffic. It then gives in-flight requests `SHUTDOWN_TIMEOUT` (125s) to finish. The pod's
`terminationGracePeriodSeconds` (150s) must exceed the two together. Raise all three together.

## Migrations

- In Kubernetes, the `migrate` init container runs `open-mcp-gateway migrate` in every new pod. A PostgreSQL advisory
  lock serialises concurrent runs, so only the first pod does any work.
- Elsewhere, run `open-mcp-gateway migrate` before `serve`, or set `MIGRATE_ON_START=true` on a single instance.
- `serve` refuses to start while migrations are pending. The message names the current and required versions.

## Scaling

- The gateway is stateless, so scale the Deployment horizontally. All shared state is in PostgreSQL.
- Background loops:
  - The MCP server health checks (`HEALTH_CHECK_INTERVAL`, default 5m) and the signing-key rotation take a PostgreSQL
    advisory lock, so one replica runs each at a time.
  - Session cleanup runs on every replica. Its deletes are idempotent.
- Each replica holds a pgx connection pool. Size Postgres `max_connections` (or a pooler) for replicas × pool size.
- Rate limits (`REQUESTS_PER_MINUTE`, `LOGIN_ATTEMPTS_PER_MINUTE`) are counted per process in this release. With N
  replicas, the effective limit is up to N times the setting.

## Upgrades

1. Pin the new image digest in the overlay.
2. **Migrate, then roll.** The new pods' `migrate` init container applies the schema before any new pod serves. Old pods
   keep serving during the rollout (`maxUnavailable: 0`), so a release's migrations must work with the previous release's
   code. Outside Kubernetes, run `migrate` with the new image first, then restart `serve`.
3. To roll back, pin the previous digest. Migrations are never rolled back automatically.

## Backups

PostgreSQL is the only state. Back it up with your usual tooling (managed snapshots, `pg_dump`, or WAL archiving). Also
keep `ENCRYPTION_KEY` (and any `ENCRYPTION_KEY_PREVIOUS`) and `API_KEY_PEPPER` somewhere safe and separate from the
backups:

- Without `ENCRYPTION_KEY`, restored MCP server header values and identity signing keys cannot be decrypted.
- Without `API_KEY_PEPPER`, every API key stops working.

## Key rotation

- **`ENCRYPTION_KEY`.** Move the current value to `ENCRYPTION_KEY_PREVIOUS`, set a new `ENCRYPTION_KEY`, and roll the
  pods. Old values still decrypt. There is no bulk re-encrypt command yet: values are re-sealed when they are re-entered
  (MCP server headers) or replaced (signing keys rotate every 30 days). Keep the previous key until both have happened.
- **`API_KEY_PEPPER`.** Changing it invalidates every API key, and there is no `_PREVIOUS`.

## Not covered yet

- No metrics endpoint, ServiceMonitor, dashboards, alerts or tracing. Logs are JSON on stdout.
- No in-cluster PostgreSQL component, backup CronJob or restore procedure. Bring your own Postgres and backups.
- No `k8s-smoke` (kind) test. `make k8s-validate` only renders the manifests and checks their schemas.
- No published image or release workflow. The example's digest is a placeholder.
- No Helm chart and no HorizontalPodAutoscaler.
- No rate limits shared across replicas, and no SSE relay. Upstream MCP servers must be on public addresses.
