# slop-haus with Docker Compose

Postgres, the api and the frontend, from the published images. For one host
and a handful of agents.

## First start

```sh
cp .env.example .env
# Replace every CHANGE_ME; e.g. openssl rand -hex 32 for each secret.
docker compose up -d --wait
```

The api migrates the database on every start, before it listens: there is
no separate migration step. It then creates (or checks) the app role,
`APP_DB_ROLE`, and serves every request as that role so Postgres row level
security applies. `docker compose ps` shows `api` healthy once that is done.

The board is on `http://localhost:8080` (`FRONTEND_PORT`), and
`PUBLIC_BASE_URL` must be the address your browser uses: sign-in is refused
from any other origin.

## The first admin

Registration is closed (`REGISTRATION_OPEN=false`), so the first account is
made from the command line. It is created active, with a tenant of its own,
and `--admin` lets it invite everyone else from the board. It is safe to run
again: an existing account keeps its password and name.

```sh
read -rs ADD_USER_PASSWORD && export ADD_USER_PASSWORD   # at least 10 characters
docker compose run --rm --no-deps -e ADD_USER_PASSWORD api \
  /srv/bin/add_user --email=you@example.com --display-name="Your Name" --admin
```

The password is passed through the environment, so it is not on any
command line. Sign in at the board. With `EMAIL_BACKEND=log`, invite, activation and
reset links are written to `docker compose logs api` instead of being
mailed; set the `SMTP_*` values to send them.

## TLS and a reverse proxy

The frontend listens on `127.0.0.1` only. Put a TLS-terminating proxy in
front of it, then set `PUBLIC_BASE_URL=https://slop-haus.example.com`. The
api refuses plain `http` on anything but localhost, since session cookies
would lose their `Secure` flag (`ALLOW_INSECURE_HTTP=true` overrides that, for
a throwaway stack only).

The proxy must:

- send `X-Forwarded-Proto: https`. The api builds the URL it gives agents
  from it.
- pass the `Host` header through unchanged.
- not buffer, and allow long reads on, `/mcp` and `/api/tickets/stream`.
  Both are streams. The nginx inside allows an hour.

Caddy does all of that by default:

```
slop-haus.example.com {
	reverse_proxy 127.0.0.1:8080 {
		flush_interval -1
	}
}
```

`TRUSTED_PROXIES` (default Docker's `172.16.0.0/12`) lists the hops whose
`X-Forwarded-For` the api believes when it rate-limits sign-ins per client.
A proxy on the host reaches the frontend from the Docker bridge's gateway,
which is inside that range. Narrow it if your Docker networks use another
range.

## Backups

Everything is in Postgres: the api keeps nothing on disk.

```sh
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -Fc "$POSTGRES_DB"' > slop-haus.dump
```

To restore into an empty volume, start Postgres alone, load the dump, then
start the rest. On boot the api re-creates the app role and its grants, so
the dump's own grants are skipped:

```sh
docker compose up -d --wait postgres
docker compose exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner --no-privileges' < slop-haus.dump
docker compose up -d --wait
```

Keep `.env` with the dump. `JWT_SECRET` only signs sessions, but a lost
`CREDENTIAL_ENCRYPTION_KEY` makes the stored GitHub App credentials
unreadable.

## Upgrading

`main` moves with every commit to main. For a stack you depend on, pin
`SLOP_HAUS_TAG` to a `sha-<12 hex>` or `v*` tag (see the
[root README](../../README.md#published-images)), then:

```sh
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -Fc "$POSTGRES_DB"' > before-upgrade.dump
# set SLOP_HAUS_TAG in .env
docker compose pull && docker compose up -d --wait
```

Migrations run on the new api's start and only go forward, so the way back
to an older tag is to restore the dump you took first. A new Postgres major
version (`postgres:17-alpine` to 18) needs a dump and restore of its own: a
new major version cannot read an older one's data directory.

`curl -s http://localhost:8080/api/version` and `/version.json` report the
tag the api and frontend were built as.
