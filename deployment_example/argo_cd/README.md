# slop-haus with Argo CD

- `application.yaml` deploys the [Helm chart](../helm/README.md) from this
  repository at a pinned revision. It reads your values from a repository
  of your own, through Argo CD's multi-source `$values` reference.
- `appproject.yaml` confines it to those two repositories, the `slop-haus`
  namespace, and the kinds the chart renders. Nothing cluster-scoped.
- `values-override.yaml` is an example of that values file.

## Setup

1. Copy `values-override.yaml` into your config repository as
   `slop-haus/values.yaml`, and set `repoURL` in both manifests to that
   repository.
2. Create the namespace and the Secret yourself. Keep secrets out of git,
   or use a secrets operator (External Secrets, Sealed Secrets) that writes
   this Secret:

   ```sh
   kubectl create namespace slop-haus
   kubectl -n slop-haus create secret generic slop-haus-secrets \
     --from-literal=DATABASE_URL='postgres://slophaus:CHANGE_ME@db.example.com:5432/slophaus?sslmode=require' \
     --from-literal=APP_DB_PASSWORD="$(openssl rand -hex 24)" \
     --from-literal=JWT_SECRET="$(openssl rand -hex 32)"
   ```

   [The chart README](../helm/README.md#settings-the-api-requires) lists the
   keys and what the api requires of each.
3. Replace the placeholders. `targetRevision` takes a release tag or a full
   commit SHA of this repository, and `image.tag` takes a `sha-<12 hex>` or
   `v*` image tag. Then apply the manifests:
   `kubectl apply -n argocd -f appproject.yaml -f application.yaml`.
4. Sync. Then create the first admin, who invites everyone else. Argo CD
   does not show the chart's `NOTES.txt`, so here is its command. It reads
   the password (at least 10 characters) from stdin:

   ```sh
   read -rs PW && printf '%s\n' "$PW" | kubectl -n slop-haus exec -i deploy/slop-haus-api -- \
     sh -c 'read -r ADD_USER_PASSWORD; export ADD_USER_PASSWORD; exec /srv/bin/add_user --email=admin@example.com --display-name=Admin --admin'
   ```

## Why sync is manual

Automated sync is off. Every sync of a new image tag runs database
migrations, and migrations only go forward: the way back is a restore from
backup. With manual sync, a merged values change waits until someone presses
Sync, after taking the backup.

Turning `automated` on gives you continuous delivery. The cost is that
whatever lands on the tracked revision is applied within minutes, migrations
included:

- `prune: true` also deletes what the chart stops rendering. For example,
  turning `postgresql.enabled` off deletes the StatefulSet (the PVC stays).
- `selfHeal: true` reverts any change made with `kubectl`.

Either is reasonable once upgrades are routine and backups are automatic.
Pinning `targetRevision` and `image.tag` keeps a deploy to a commit you
chose, whichever mode you run.

## Checks

`../test.sh` validates both manifests against the Argo CD CRD schemas, from
the datree CRDs-catalog at a fixed commit, with kubeconform. It also renders
the chart with `values-override.yaml`.
