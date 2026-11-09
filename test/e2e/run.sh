#!/usr/bin/env bash
set -euo pipefail

cluster=openbao-rotation-exporter-e2e
image=openbao-rotation-exporter:e2e
# renovate: datasource=github-releases depName=prometheus-operator/prometheus-operator
prometheus_operator_version=v0.94.2

cleanup() {
  status=$?
  if (( status != 0 )); then
    kubectl get pods --all-namespaces || true
    kubectl --namespace rotation describe pods || true
    kubectl --namespace rotation logs deployment/openbao-rotation-exporter || true
    kubectl --namespace openbao logs deployment/openbao --tail=50 || true
  fi
  kind delete cluster --name "${cluster}"
  exit "${status}"
}

kind create cluster --name "${cluster}" --wait 180s
trap cleanup EXIT

docker build --build-arg VERSION=e2e --tag "${image}" .
kind load docker-image --name "${cluster}" "${image}"

kubectl apply --server-side \
  --filename "https://github.com/prometheus-operator/prometheus-operator/releases/download/${prometheus_operator_version}/stripped-down-crds.yaml"

kubectl apply --filename test/e2e/openbao.yaml
kubectl --namespace openbao rollout status deployment/openbao --timeout=180s

bao() {
  kubectl --namespace openbao exec --stdin deployment/openbao -- bao "$@"
}

bao secrets enable -path=e2e -version=2 kv
bao kv put e2e/app/database value=do-not-read
bao auth enable kubernetes
bao write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc
sed 's/MOUNT/e2e/' examples/policy.hcl | bao policy write rotation -
bao write auth/kubernetes/role/openbao-rotation-exporter \
  bound_service_account_names=openbao-rotation-exporter \
  bound_service_account_namespaces=rotation \
  token_ttl=5m \
  token_max_ttl=5m \
  token_policies=rotation

kubectl apply --filename test/e2e/namespace.yaml
helm install openbao-rotation-exporter charts/openbao-rotation-exporter \
  --namespace rotation \
  --values test/e2e/values.yaml
kubectl --namespace rotation rollout status deployment/openbao-rotation-exporter --timeout=180s

metrics=""
for _ in {1..30}; do
  metrics=$(kubectl get --raw /api/v1/namespaces/rotation/services/openbao-rotation-exporter:metrics/proxy/metrics || true)
  if grep --fixed-strings --quiet 'openbao_rotation_collection_success 1' <<< "${metrics}"; then
    break
  fi
  sleep 2
done

for expected in \
  'openbao_rotation_build_info{version="e2e"} 1' \
  'openbao_rotation_collection_success 1' \
  'openbao_rotation_key_up{key="app/database"} 1' \
  'openbao_rotation_key_due_timestamp_seconds{key="app/database"}' \
  'openbao_rotation_key_up{key="app/missing"} 0' \
  'openbao_rotation_key_failures_total{key="app/missing",reason="not_found"}'; do
  if ! grep --fixed-strings --quiet "${expected}" <<< "${metrics}"; then
    echo "missing from /metrics: ${expected}" >&2
    echo "${metrics}" >&2
    exit 1
  fi
done

kubectl --namespace rotation get servicemonitor/openbao-rotation-exporter prometheusrule/openbao-rotation-exporter

echo "e2e passed"
