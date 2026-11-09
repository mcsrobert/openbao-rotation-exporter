#!/usr/bin/env bash
set -euo pipefail

chart=charts/openbao-rotation-exporter
values=test/chart/values.yaml
release=openbao-rotation-exporter
namespace=rotation

work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT

render() {
  helm template "${release}" "${chart}" --namespace "${namespace}" --values "${values}" "$@"
}

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

expect_equal() {
  if [[ "$2" != "$3" ]]; then
    fail "$1: expected '$3', got '$2'"
  fi
}

expect_rejected() {
  local description=$1
  shift
  if helm template "${release}" "${chart}" --namespace "${namespace}" "$@" > /dev/null 2>&1; then
    fail "the chart accepted ${description}"
  fi
}

helm lint --strict "${chart}" --values "${values}"

render --set networkPolicy.enabled=true | kubeconform \
  -strict \
  -summary \
  -schema-location default \
  -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

render --show-only templates/prometheus-rule.yaml | yq '.spec' > "${work}/alerts.yaml"
cp test/chart/alerts.test.yaml "${work}/"
promtool check rules "${work}/alerts.yaml"
(cd "${work}" && promtool test rules alerts.test.yaml)

deployment_image() {
  render --show-only templates/deployment.yaml "$@" | yq '.spec.template.spec.containers[0].image'
}
digest=sha256:0000000000000000000000000000000000000000000000000000000000000000
image=ghcr.io/mcsrobert/openbao-rotation-exporter
expect_equal "image without a digest" "$(deployment_image)" "${image}:dev"
expect_equal "image with a digest" "$(deployment_image --set image.tag=1.2.3 --set "image.digest=${digest}")" "${image}:1.2.3@${digest}"

expect_equal "rendered configuration" \
  "$(render --show-only templates/config-map.yaml | yq '.data["config.yaml"]' | yq --output-format=json --indent=0 'sort_keys(..)')" \
  "$(yq --output-format=json --indent=0 '.config | sort_keys(..)' "${values}")"

checksum() {
  render --show-only templates/deployment.yaml "$@" | yq '.spec.template.metadata.annotations["checksum/config"]'
}
if [[ "$(checksum)" == "$(checksum --set config.keys.app/database=30d)" ]]; then
  fail "changing the configuration does not restart the pod"
fi

kinds() {
  render "$@" | yq --no-doc '.kind' | sort | paste -sd, -
}
expect_equal "default resources" "$(kinds)" "ConfigMap,Deployment,PrometheusRule,Service,ServiceAccount,ServiceMonitor"
expect_equal "optional resources disabled" \
  "$(kinds --set serviceMonitor.enabled=false --set prometheusRule.enabled=false --set serviceAccount.create=false)" \
  "ConfigMap,Deployment,Service"
expect_equal "network policy enabled" \
  "$(kinds --set networkPolicy.enabled=true | tr ',' '\n' | grep -c NetworkPolicy)" \
  "1"

other() {
  helm template other "${chart}" --namespace elsewhere --values "${values}" "$@"
}
expect_equal "service name of another release" \
  "$(other --show-only templates/service.yaml | yq '.metadata.name')" \
  "other-openbao-rotation-exporter"
expect_equal "alert scope of another release" \
  "$(other --show-only templates/prometheus-rule.yaml | yq '.spec.groups[0].rules[1].expr')" \
  'max by (job, namespace, key) (openbao_rotation_key_up{job="other-openbao-rotation-exporter",namespace="elsewhere"}) == 0'

expect_rejected "missing configuration"
expect_rejected "an unknown value" --values "${values}" --set unknown=true
expect_rejected "an unknown configuration field" --values "${values}" --set config.unknown=true
expect_rejected "no keys" --values "${values}" --set 'config.keys=null'
expect_rejected "an invalid interval" --values "${values}" --set config.keys.app/database=90m
expect_rejected "a zero interval" --values "${values}" --set config.keys.app/database=0d
expect_rejected "two auth methods" --values "${values}" --set config.openbao.auth.tokenFile=/token
expect_rejected "an invalid digest" --values "${values}" --set image.digest=latest

echo "chart tests passed"
