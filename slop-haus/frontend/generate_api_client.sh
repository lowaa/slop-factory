#!/usr/bin/env bash
# Regenerates src/api_generated/{openapi.json,schema.d.ts} from
# slop-haus/server's real route registration (server/internal/apiserver),
# in two steps:
#   1. Go (server/cmd/generate_openapi_spec) writes the OpenAPI 3.1 JSON
#      document.
#   2. openapi-typescript (a devDependency) turns it into TypeScript types,
#      schema.d.ts, which src/api/index.ts calls the server through.
# Both files are committed, so the frontend builds without Go. Run this after
# changing a server route or wire body, and commit the result.
#
#   ./generate_api_client.sh           write both files
#   ./generate_api_client.sh --check   fail if either is stale, writing
#                                      nothing (what CI runs)
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
TARGET_DIR="${SCRIPT_DIR}/src/api_generated"
SERVER_DIR="${SCRIPT_DIR}/../server"
SPEC_FILE="${TARGET_DIR}/openapi.json"
SCHEMA_FILE="${TARGET_DIR}/schema.d.ts"

case "${1:-}" in
  "") check=0 ;;
  --check) check=1 ;;
  *)
    echo "usage: $0 [--check]" >&2
    exit 2
    ;;
esac

if ! command -v go &> /dev/null; then
  echo "generate_api_client.sh: go is required to generate ${SPEC_FILE}" >&2
  exit 1
fi

# Run from the frontend workspace, so npx finds its own openapi-typescript.
openapi_typescript() {
  (cd "${SCRIPT_DIR}" && npx --no-install openapi-typescript "$@")
}

if [[ "${check}" == "1" ]]; then
  (cd "${SERVER_DIR}" && go run ./cmd/generate_openapi_spec -o "${SPEC_FILE}" -check)

  tmp="$(mktemp)"
  trap 'rm -f "${tmp}"' EXIT
  openapi_typescript "${SPEC_FILE}" -o "${tmp}" > /dev/null
  if ! diff -q "${tmp}" "${SCHEMA_FILE}" > /dev/null 2>&1; then
    echo "generate_api_client.sh: ${SCHEMA_FILE} is out of date" >&2
    echo "run slop-haus/frontend/generate_api_client.sh and commit the result" >&2
    exit 1
  fi
  exit 0
fi

mkdir -p "${TARGET_DIR}"
(cd "${SERVER_DIR}" && go run ./cmd/generate_openapi_spec -o "${SPEC_FILE}")
openapi_typescript "${SPEC_FILE}" -o "${SCHEMA_FILE}"
