# Deployment examples

Each example runs slop-haus from its [published images](../README.md#published-images)
(the api, the frontend, and Postgres) with placeholders where your values go:

| Folder | For |
| --- | --- |
| [`docker_compose/`](docker_compose/README.md) | one host |
| [`helm/`](helm/README.md) | Kubernetes: a chart with optional Ingress, in-cluster Postgres and NetworkPolicies |
| [`argo_cd/`](argo_cd/README.md) | that chart through Argo CD |

The OTel collector the dev stack runs is optional here. The api always logs
to stderr, and it also exports to any OTLP/HTTP endpoint named by
`LOGS_OTLP_ENDPOINT`. `slop-haus/_dev_env/otel/` has the collector
configuration the project uses.

`test.sh` checks the examples without deploying them, and CI runs it. It
validates the compose file, lints the chart, and validates the chart's
renders and the Argo CD manifests against their schemas. `scan.sh` is the
security scan (`dev-tools/security/README.md`).
