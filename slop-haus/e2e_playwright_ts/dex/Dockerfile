# Dex with the e2e stack's configuration baked in, rather than bind-mounted:
# the default (containerized) e2e run then mounts nothing from the checkout,
# so it runs the same wherever the Docker daemon is -- including from a CI
# step, whose checkout the daemon can't see. ../docker-compose-dev.e2e.attach.yaml
# mounts the file over this copy for iterating on it.
FROM ghcr.io/dexidp/dex:v2.45.1

COPY config.yaml /etc/dex/config.yaml

CMD ["dex", "serve", "/etc/dex/config.yaml"]
