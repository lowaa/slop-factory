# Fixture for reap_label.bats: every command here must be flagged, by line.
# docker network create commented
a() { docker network create n; }
b() { docker create img; }
c() { docker build -t x .; }
d() { docker run -d --rm img; }
e() { docker run img; }
f() { docker run --rm \
  img; }
