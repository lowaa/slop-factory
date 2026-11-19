#!/usr/bin/env bash
# Checks the deployment examples still render and validate: the compose file
# against its .env.example, the Helm chart (lint, then every values file in
# ci/ and the Argo CD override rendered and validated against the Kubernetes
# schemas), and the Argo CD manifests against the Argo CD CRDs' schemas.
# Static only: nothing is deployed. Needs docker; helm and kubeconform run
# from pinned images, so a local run and CI's are the same.
set -euo pipefail
# shellcheck source=SCRIPTDIR/../dev-tools/require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../dev-tools/require_bash.sh"
shopt -s inherit_errexit

# shellcheck source=SCRIPTDIR/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
KUBECONFORM_IMAGE="ghcr.io/yannh/kubeconform:v0.7.0@sha256:85dbef6b4b312b99133decc9c6fc9495e9fc5f92293d4ff3b7e1b30f5611823c"
KUBERNETES_VERSION="1.31.0"
# The datree CRDs-catalog at a fixed commit, for the Argo CD kinds.
CRD_SCHEMAS="https://raw.githubusercontent.com/datreeio/CRDs-catalog/784ec895547a36e548e6810b41f27baade3a0890/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json"

kubeconform() {
  docker run --rm -i "${KUBECONFORM_IMAGE}" -strict -summary \
    -kubernetes-version "${KUBERNETES_VERSION}" \
    -schema-location default -schema-location "${CRD_SCHEMAS}"
}

# expect_render_failure VALUES... MESSAGE: the chart refuses those values,
# naming MESSAGE: values the api would refuse to start on.
expect_render_failure() {
  local message="${*: -1}" out
  local -a sets=("${@:1:$#-1}")
  if out="$(helm template t "${CHART}" -f "${CHART}/ci/minimal-values.yaml" "${sets[@]}" 2>&1)"; then
    echo "FAIL: rendered with ${sets[*]}, expected: ${message}" >&2
    return 1
  fi
  [[ "${out}" == *"${message}"* ]] || {
    echo "FAIL: ${sets[*]} failed, but not with '${message}':" >&2
    echo "${out}" >&2
    return 1
  }
}

echo "== docker compose config"
docker compose --project-directory "${HERE}/docker_compose" -f "${HERE}/docker_compose/compose.yaml" \
  --env-file "${HERE}/docker_compose/.env.example" config --quiet

echo "== helm lint"
for values in "${HERE}/${CHART}"/ci/*-values.yaml; do
  helm lint --strict "${CHART}" -f "${CHART}/ci/$(basename "${values}")"
done

echo "== helm template | kubeconform"
for values in "${HERE}/${CHART}"/ci/*-values.yaml; do
  echo "-- $(basename "${values}")"
  helm template t "${CHART}" -n slop-haus -f "${CHART}/ci/$(basename "${values}")" | kubeconform
done
echo "-- argo_cd/values-override.yaml"
helm template slop-haus "${CHART}" -n slop-haus -f argo_cd/values-override.yaml | kubeconform

echo "== the chart refuses the commonest values the api would"
webhook=(--set api.webhook.enabled=true --set api.credentials.enabled=true --set secrets.credentialEncryptionKey=placeholder)
expect_render_failure --set publicBaseUrl= "publicBaseUrl is required"
expect_render_failure --set publicBaseUrl=http://slop-haus.example.com "plain http on a non-loopback host"
expect_render_failure --set secrets.jwtSecret=too-short "at least 32 bytes"
expect_render_failure --set api.email.backend=smpt "must be log or smtp"
expect_render_failure --set api.passwordSignIn=false "nobody can sign in"
expect_render_failure --set api.webhook.enabled=true --set api.webhook.publicBaseUrl=https://hooks.example.com "needs api.credentials.enabled"
expect_render_failure "${webhook[@]}" --set api.webhook.publicBaseUrl=https://hooks.example.com/github "origin with no path"
expect_render_failure "${webhook[@]}" --set api.webhook.publicBaseUrl=http://hooks.example.com "must be https"
expect_render_failure --set api.oidc.issuerUrl=https://sso.example.com --set secrets.oidcClientSecret=x "must be set together"
expect_render_failure --set api.intake.enabled=true --set api.intake.publicBaseUrl=http://intake:8082 --set secrets.intakeKey=short "secrets.intakeKey must be at least 32 bytes"
helm template t "${CHART}" -f "${CHART}/ci/minimal-values.yaml" --set publicBaseUrl=http://localhost:8080 > /dev/null

echo "== ingress hosts default to their base URLs"
hosts="$(helm template t "${CHART}" -f "${CHART}/ci/minimal-values.yaml" "${webhook[@]}" \
  --set api.webhook.publicBaseUrl=https://hooks.example.com --set ingress.enabled=true --set api.webhook.ingress.enabled=true |
  grep -E '^    - host: ' | sort | tr -d '\n')"
[[ "${hosts}" == '    - host: "hooks.example.com"    - host: "slop-haus.example.com"' ]] || {
  echo "FAIL: ingress hosts were: ${hosts}" >&2
  exit 1
}

echo "== argo_cd manifests"
kubeconform < <(cat "${HERE}/argo_cd/application.yaml" <(echo ---) "${HERE}/argo_cd/appproject.yaml")

echo "deployment_example: ok"
