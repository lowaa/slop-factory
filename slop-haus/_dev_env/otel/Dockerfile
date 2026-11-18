# The collector with the dev stack's configuration baked in: nothing is
# mounted from the checkout, so it runs the same wherever the Docker daemon
# is, including from a CI step whose checkout the daemon can't see.
FROM otel/opentelemetry-collector-contrib:0.140.0

COPY otel-collector-config.yaml /etc/otel-collector-config.yaml
