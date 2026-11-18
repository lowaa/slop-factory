# slop-haus Helm chart

`slop-haus/` deploys the api and the frontend, and optionally a small
Postgres. Every value is described in
[`slop-haus/values.yaml`](slop-haus/values.yaml).

## What it creates

- **api**: Deployment and Service on 8080. It runs as uid 65532 with a
  read-only root filesystem and no capabilities. The startup probe waits on
  `/api/version` while migrations run. Readiness is `/healthz`, which pings
  the database. Liveness is `/api/version`, so a database outage takes pods
  out of the Service without restarting them. Extra listeners, each with its
  own Service, when their feature is on: webhook 8081, intake 8082, metrics
  9090.
- **frontend**: Deployment and Service on 80. nginx serves the board and
  proxies `/api`, `/mcp`, `/hooks`, `/oauth` and the OAuth discovery
  documents to the api. Probes use `/version.json`. The official nginx image
  starts as root to bind :80, so this pod is not `runAsNonRoot`. It keeps
  only the CHOWN, SETUID, SETGID and NET_BIND_SERVICE capabilities, and a
  read-only root filesystem.
- **ConfigMap** with the api's settings and a **Secret** with its secrets
  (unless you bring your own). Changing either rolls the api pods.
- Optional: an **Ingress** to the frontend (one host, one path; the api is
  never routed directly), a second Ingress for the GitHub webhook receiver,
  a single-instance **Postgres** StatefulSet, and **NetworkPolicies**.

Both pods sleep 5s in `preStop`, so the Service drops them before they stop
accepting connections. The api's grace period, 30s, covers the drain and the
log flush.

## Install

```sh
kubectl create namespace slop-haus
kubectl -n slop-haus create secret generic slop-haus-secrets \
  --from-literal=DATABASE_URL='postgres://slophaus:CHANGE_ME@db.example.com:5432/slophaus?sslmode=require' \
  --from-literal=APP_DB_PASSWORD="$(openssl rand -hex 24)" \
  --from-literal=JWT_SECRET="$(openssl rand -hex 32)"

helm install slop-haus ./slop-haus -n slop-haus \
  --set publicBaseUrl=https://slop-haus.example.com \
  --set secrets.existingSecret=slop-haus-secrets \
  --set image.tag=v1.2.3 \
  --set ingress.enabled=true --set ingress.host=slop-haus.example.com
```

`NOTES.txt` prints the next steps, including the first admin.

## Settings the api requires

The api refuses to start without these. The chart checks them, and the
commonest invalid combinations, at render time, so those mistakes fail
`helm install` instead of looping the api pod:

| Setting | Value | Notes |
| --- | --- | --- |
| `DATABASE_URL` | Secret | The owner role's DSN. The api migrates as this role and creates the app role, so it needs `CREATEROLE`, or must be a superuser. Built from `postgresql.*` when the in-cluster Postgres is on. |
| `APP_DB_PASSWORD` | Secret | The app role's password. The api creates the role, `database.appRole` (default `slophaus_app`), and resets its password to this value on every start. The role must not have SUPERUSER or BYPASSRLS. |
| `JWT_SECRET` | Secret | Signs sessions. Must be at least 32 bytes. |
| `PUBLIC_BASE_URL` | `publicBaseUrl` | Must be https, or http on localhost only. |
| `EMAIL_BACKEND` | `api.email.backend` | `log` (the default here) or `smtp`, which also needs `host` and `from`. |

The optional features are values: SMTP, GitHub and OIDC sign-in, GitHub App
credentials (`api.credentials`, which needs `CREDENTIAL_ENCRYPTION_KEY`), the
webhook and intake doors, S3 attachments, metrics, and OTLP log export. A
feature's secret is read only when that feature is on. Any other setting in
`slop-haus/server/internal/config/config.go` goes in `api.extraEnv`.

`api.trustedProxies` should be your pod network's CIDR (k3s:
`10.42.0.0/16`). It lists the hops (the ingress controller and the frontend)
whose `X-Forwarded-For` the api believes when it rate-limits sign-ins.

`frontend.dnsResolver` must name your cluster DNS Service. The default,
`kube-dns.kube-system.svc.cluster.local`, works on most distributions. nginx
resolves the api on each request, so the frontend starts before the api is
up.

## Postgres

External by default. The dev stack and these examples run Postgres 17. Give the owner role
`CREATEROLE` and use `sslmode=require` or stricter. Managed Postgres
services and [CloudNativePG](https://cloudnative-pg.io/) both work. With
CloudNativePG, run `ALTER ROLE app CREATEROLE` in `postInitSQL`.

`postgresql.enabled=true` adds one `postgres:17-alpine` pod with a PVC, for
trying the chart or a small install. It has no replicas and no backups. Back
it up with `pg_dump`:

```sh
kubectl -n slop-haus exec slop-haus-postgresql-0 -- sh -c 'pg_dump -U "$POSTGRES_USER" -Fc "$POSTGRES_DB"' > slop-haus.dump
```

(A Bitnami-style chart dependency would work too. This chart avoids one,
since those images and charts have changed distribution terms.)

## Upgrading

Pin `image.tag` to a `v*` tag. `main` (the chart's `appVersion`) moves with
every commit, and it is pulled `Always`. A `sha-<12 hex>` tag is deleted once
it is older than 14 days and outside the newest 20. After that, a pod
rescheduled to a new node cannot pull it. Use one only short-term, or mirror
it to your own registry (`image.*.repository`). `image.api.digest` and
`image.frontend.digest` pin exactly which image a tag resolves to, but they
do not keep it from being pruned. On an
upgrade the new api pod runs the migrations while the old pod still serves,
then takes over once it is ready. Migrations only go forward, so take a
backup first. Rolling back means restoring that backup.

## Checks

`../test.sh` lints the chart and validates its rendered output against the
Kubernetes schemas with kubeconform, once for each file in `slop-haus/ci/`.
It also checks that the chart refuses the commonest values the api would
refuse at startup. The api's own check (`config.go`) remains the full one. CI runs
the same script.
