# openbao-rotation-exporter chart

Deploys the
[OpenBao rotation exporter](https://github.com/mcsrobert/openbao-rotation-exporter)
with a `ServiceMonitor` and alert rules. The exporter's README describes its
behavior and the OpenBao policy it needs.

## Install

```sh
helm install openbao-rotation-exporter \
  oci://ghcr.io/mcsrobert/charts/openbao-rotation-exporter \
  --version <version> \
  --namespace rotation \
  --values values.yaml
```

A minimal `values.yaml`:

```yaml
---
config:
  openbao:
    address: https://openbao.example.com
    mount: secret
    auth:
      kubernetes:
        role: openbao-rotation-exporter
  keys:
    app/database: 90d
    app/api-token: 26w
```

The Kubernetes auth role must bind the release's service account and namespace.
By default, its name is `<release>-openbao-rotation-exporter`, for example
`other-openbao-rotation-exporter` for release `other`. If the release name already
contains `openbao-rotation-exporter`, that release name is used directly.
`nameOverride` and `fullnameOverride` affect the generated name; set
`serviceAccount.name` to choose an explicit name. With `serviceAccount.create=false`,
the chart uses `serviceAccount.name`, or `default` when no name is provided.

`ServiceMonitor` and `PrometheusRule` are on by default and need the
`monitoring.coreos.com` CRDs. Without them, set `serviceMonitor.enabled` and
`prometheusRule.enabled` to `false`.

## Values

| Value | Default | Description |
| --- | --- | --- |
| `config` | required | The exporter's configuration file, as documented in its README |
| `image.repository` | `ghcr.io/mcsrobert/openbao-rotation-exporter` | Image repository |
| `image.tag` | Chart's `appVersion` | Image tag |
| `image.digest` | The digest of the release | Image digest. Clear or replace it when you change the tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `imagePullSecrets` | `[]` | Pull secrets |
| `serviceAccount.create` | `true` | Create a service account |
| `serviceAccount.name` | Full name of the release | Service account name |
| `serviceAccount.annotations` | `{}` | Service account annotations |
| `serviceAccount.automount` | `true` | Mount the token. Kubernetes auth needs it |
| `service.port` | `8080` | Service port |
| `service.annotations` | `{}` | Service annotations |
| `serviceMonitor.enabled` | `true` | Create a `ServiceMonitor` |
| `serviceMonitor.labels` | `{}` | Extra labels |
| `serviceMonitor.interval` | Scraper default | Scrape interval |
| `serviceMonitor.scrapeTimeout` | Scraper default | Scrape timeout |
| `prometheusRule.enabled` | `true` | Create a `PrometheusRule` |
| `prometheusRule.labels` | `{}` | Extra labels on the resource |
| `prometheusRule.alertLabels` | `severity: warning` | Labels on each alert |
| `networkPolicy.enabled` | `false` | Limit ingress to the metrics port |
| `networkPolicy.from` | `[]` | Allowed peers. Empty allows all sources |
| `resources` | 10m CPU, 32Mi request, 64Mi limit | Container resources |
| `podSecurityContext` | Non-root, `RuntimeDefault` seccomp | Pod security context |
| `securityContext` | Read-only root, no capabilities | Container security context |
| `podAnnotations`, `podLabels` | `{}` | Extra pod metadata |
| `priorityClassName` | `""` | Priority class |
| `nodeSelector`, `tolerations`, `affinity` | Empty | Scheduling |
| `extraVolumes`, `extraVolumeMounts` | `[]` | For `openbao.caFile` or `auth.tokenFile` |
| `nameOverride`, `fullnameOverride` | `""` | Resource names |

The chart runs one replica. `OpenBaoRotationDue` and `OpenBaoRotationKeyFailing`
aggregate each key across pods, so a rollout does not resolve them and fire
them again for the new pod. While two pods overlap, the latest due time counts,
and a key only fails when no pod can read it.

## Alerts

The rules only match this release's job and namespace, so several releases can
share a cluster. Alertmanager should use a long `repeat_interval` for
`OpenBaoRotationDue`, because it fires until a new version is written.

| Alert | Fires when |
| --- | --- |
| `OpenBaoRotationDue` | A key has passed its due time for 15 minutes |
| `OpenBaoRotationKeyFailing` | A key's metadata could not be read for 15 minutes |
| `OpenBaoRotationCollectionStale` | No collection has succeeded for three collection intervals, or 30 minutes if that is longer, plus 5 minutes |

Use the monitoring stack's default target and workload alerts to detect an
unavailable exporter. Inhibit collection-stale and key-failing notifications
while an OpenBao-down alert fires.

The due alert uses the exporter's in-memory deadlines. If it restarts while
OpenBao is unavailable, due alerts may resolve until successful reads restore
the deadlines. An overdue key then fires again after the 15-minute pending
period. Stale collection timing starts at process start until the first
successful collection. Removing a key removes its deadline from the exporter.

To use a token limited to OpenBao, set `serviceAccount.automount: false`.
Mount a projected `serviceAccountToken` using `extraVolumes` and
`extraVolumeMounts`. Set `config.openbao.auth.kubernetes.jwtFile` to the token
path. Set the same audience (for example `openbao`) on the projection and the
OpenBao Kubernetes role. Kubernetes rotates the projected token; the exporter
rereads it on login.
