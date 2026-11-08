#!/usr/bin/env bash
set -euo pipefail

image=openbao-rotation-exporter:smoke
name=openbao-rotation-exporter-smoke
port=18080

docker build --build-arg VERSION=smoke --tag "${image}" .

if [[ "$(docker run --rm "${image}" --version)" != smoke ]]; then
  echo "unexpected version" >&2
  exit 1
fi

if [[ "$(docker inspect --format '{{.Config.User}}' "${image}")" != 65532:65532 ]]; then
  echo "image does not run as the nonroot user by default" >&2
  exit 1
fi

directory=$(mktemp -d)
cleanup() {
  docker logs "${name}" 2>&1 || true
  docker rm --force "${name}" > /dev/null 2>&1 || true
  rm -rf "${directory}"
}
trap cleanup EXIT

cat > "${directory}/config.yaml" <<'EOF'
---
openbao:
  address: http://127.0.0.1:1
  mount: secret
  auth:
    tokenFile: /nonexistent
keys:
  app/database: 90d
EOF
chmod 755 "${directory}"
chmod 644 "${directory}/config.yaml"

docker run --detach --name "${name}" \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --publish "127.0.0.1:${port}:8080" \
  --volume "${directory}:/etc/openbao-rotation-exporter:ro" \
  "${image}"

for _ in {1..30}; do
  if curl --fail --silent "http://127.0.0.1:${port}/readyz"; then
    break
  fi
  sleep 1
done

curl --fail --silent --show-error "http://127.0.0.1:${port}/healthz"
metrics=$(curl --fail --silent --show-error "http://127.0.0.1:${port}/metrics")

for expected in \
  'openbao_rotation_build_info{version="smoke"} 1' \
  'openbao_rotation_collection_success 0' \
  'openbao_rotation_last_success_timestamp_seconds 0' \
  'openbao_rotation_key_up{key="app/database"} 0' \
  'openbao_rotation_collection_failures_total{reason="credentials"} 1'; do
  if ! grep --fixed-strings --quiet "${expected}" <<< "${metrics}"; then
    echo "missing from /metrics: ${expected}" >&2
    echo "${metrics}" >&2
    exit 1
  fi
done
