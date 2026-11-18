# Fixture for reap_label.bats: nothing here may be flagged.
a() { docker network create --label "${REAP_LABEL}" n; }
b() { docker create --name x \
  --label "${REAP_COMPOSE_LABEL}=p" img; }
c() { docker build --label "${IMG_REAP_LABEL}" .; }
d() { docker run -d --rm --label "${REAP_LABEL}" img; }
e() { docker run --rm img; }
