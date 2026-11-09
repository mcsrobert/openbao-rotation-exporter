# OpenBao rotation exporter

A Prometheus exporter that tells you when OpenBao KV v2 secrets are due for
rotation.

You list the keys you care about and how often each should rotate. The exporter
reads each key's metadata and publishes when it is due, so you can alert on it.
It reads metadata only, never secret values, and it does not rotate anything.

> [!IMPORTANT]
> The age of a key is the age of its current KV version. **A new version resets
> the reminder, but it does not prove the credential was rotated.** Any write
> resets the age, including one that changes a different property of the same
> key or rewrites the same value. Treat a reminder as "nothing has been written
> for this long". Metadata-only changes, such as `custom_metadata`, do not reset
> it.

## Configuration

The exporter reads a YAML file, by default
`/etc/openbao-rotation-exporter/config.yaml`. Unknown fields are rejected.
Restart the exporter to apply changes.

```yaml
---
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

| Field | Default | Description |
| --- | --- | --- |
| `openbao.address` | required | OpenBao URL |
| `openbao.mount` | required | KV v2 mount |
| `openbao.caFile` | system roots | PEM file of CAs to trust |
| `openbao.auth.tokenFile` | | File holding a token, reread for every collection |
| `openbao.auth.kubernetes.role` | required | Kubernetes auth role |
| `openbao.auth.kubernetes.mount` | `kubernetes` | Kubernetes auth mount |
| `openbao.auth.kubernetes.jwtFile` | The pod's service account token | JWT to log in with |
| `collection.interval` | `5m` | Time between collections |
| `collection.timeout` | `1m` or the interval, if shorter | Limit for one collection, at most the interval |
| `collection.concurrency` | `8` | Simultaneous key reads |
| `keys` | required | Key paths relative to the mount, with intervals |

Set exactly one of `auth.tokenFile` and `auth.kubernetes`. Intervals are a
positive whole number followed by `h`, `d` or `w`.

The command line accepts `--config`, `--listen-address` (default `:8080`) and
`--version`.

## OpenBao access

The exporter needs read access to metadata and permission to revoke its own
token, and nothing else. Replace `MOUNT` in
[`examples/policy.hcl`](./examples/policy.hcl) with your mount:

```hcl
path "MOUNT/metadata/*" {
  capabilities = ["read"]
}

path "auth/token/revoke-self" {
  capabilities = ["update"]
}
```

OpenBao's `default` policy also allows `auth/token/revoke-self`, but a role with
`token_no_default_policy` does not get it.

With Kubernetes auth, a collection logs in once and revokes the token through
`auth/token/revoke-self` after its reads, so tokens do not pile up. Revocation is
best effort: a failure logs a warning and does not fail the collection. Batch
tokens cannot be revoked, so they are left to expire. A token from
`auth.tokenFile` belongs to you and is never revoked.

Keep the role's `token_ttl` above `collection.timeout`, and short, so a token
that could not be revoked expires soon. Five minutes works with the defaults.

The client does not follow redirects, so a redirect cannot send the token to
another host. Point `openbao.address` at a server that answers directly.

## Metrics

Scrape `/metrics`. The endpoints only serve the latest snapshot and never
contact OpenBao, so scrapes are cheap and cannot trigger logins.

| Metric | Description |
| --- | --- |
| `openbao_rotation_key_due_timestamp_seconds{key}` | Current version's creation time plus the interval |
| `openbao_rotation_key_up{key}` | Whether the latest read of the key succeeded |
| `openbao_rotation_key_failures_total{key,reason}` | Failed reads of the key |
| `openbao_rotation_collection_success` | Whether the latest collection succeeded |
| `openbao_rotation_last_success_timestamp_seconds` | Last successful collection, or 0 if none since start |
| `openbao_rotation_collection_duration_seconds` | Duration of the latest collection |
| `openbao_rotation_collection_interval_seconds` | Configured `collection.interval` |
| `openbao_rotation_collection_failures_total{reason}` | Failed collections |
| `openbao_rotation_build_info{version}` | Always 1 |

Go runtime and process metrics are also exposed.

A collection succeeds when it logs in and reads at least one key. Failure
reasons are `credentials`, `forbidden`, `http_error`, `invalid_response`,
`not_found`, `timeout`, `unavailable` and `version_unavailable`, plus
`all_keys_failed` for collections. Details are in the logs, which never contain
tokens or OpenBao response bodies.

Key paths are metric labels, so anyone who can scrape the exporter can see them.

## Failure behavior

Missing or stale data must not make an overdue secret look healthy.

| Event | Exporter | Chart alert |
| --- | --- | --- |
| One key fails | Keeps its last known due time, `key_up` is 0 | `OpenBaoRotationDue` keeps firing, `OpenBaoRotationKeyFailing` fires |
| Whole collection fails | Still returns 200 with the last snapshot, `collection_success` is 0 | `OpenBaoRotationCollectionStale` fires after the longer of 30 minutes and three intervals, the exporter is not reported as down |
| Restart, then first collection fails | No due series, `key_up` is 0, last success is 0 | `OpenBaoRotationCollectionStale` and `OpenBaoRotationKeyFailing` fire |
| Key removed from the config | Series goes away | None |
| Current version deleted or destroyed | Keeps its last known due time, `key_up` is 0 | `OpenBaoRotationKeyFailing` fires |
| Exporter cannot be scraped | | `OpenBaoRotationExporterDown` fires |

A version with a future deletion time, from `delete_version_after`, still counts
as live.

## Endpoints

| Path | Description |
| --- | --- |
| `/metrics` | Prometheus metrics |
| `/healthz` | 200 while the process is running |
| `/readyz` | 200 once a collection has finished, even a failed one |

Readiness does not depend on OpenBao being healthy. Otherwise an OpenBao outage
would stop scrapes and hide the failure metrics.

## Deployment

Install the Helm chart from GHCR:

```sh
helm install openbao-rotation-exporter \
  oci://ghcr.io/mcsrobert/charts/openbao-rotation-exporter \
  --version <version> \
  --namespace rotation \
  --values values.yaml
```

It creates the Deployment, a `ServiceMonitor` and alert rules. The
[chart README](./charts/openbao-rotation-exporter/README.md) lists its values
and alerts.

Each release publishes an image and a chart with the same version. Both are for
`linux/amd64` and `linux/arm64` hosts, and version tags are never overwritten.
The chart pins the image by digest. The image runs as a non-root user and works
with a read-only root filesystem and no capabilities.

Verify the build provenance of either one:

```sh
gh attestation verify \
  oci://ghcr.io/mcsrobert/openbao-rotation-exporter:<version> \
  --repo mcsrobert/openbao-rotation-exporter
gh attestation verify \
  oci://ghcr.io/mcsrobert/charts/openbao-rotation-exporter:<version> \
  --repo mcsrobert/openbao-rotation-exporter
```

An overdue key keeps its alert firing until a new version is written, so set a
long `repeat_interval` for these alerts in Alertmanager.

## Scope

This project reminds you about rotation. It does not rotate credentials, read
secret values or watch OpenBao's own tokens. It supports one server and one KV
v2 mount per instance.

## Development

```sh
mise run setup
mise run lint
mise run test
mise run test:integration
mise run test:chart
```

`mise run test:image` and `mise run test:e2e` also need Docker.

## Release package access

GHCR creates new packages as private, even for a public repository. The image
`mcsrobert/openbao-rotation-exporter` and chart
`mcsrobert/charts/openbao-rotation-exporter` are separate packages; making the
image public does not make the chart public.

After the first publication, a package owner must open each package under the
[mcsrobert Packages page](https://github.com/mcsrobert?tab=packages), select
**Package settings**, then **Change visibility** and **Public**. See
[GitHub's package visibility documentation](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility).
This is a package setting, not a chart value or repository permission.

The release workflow verifies anonymous pulls of both artifacts. If the chart
check fails with `403 Forbidden` or `DENIED`, check the chart package's visibility
separately. After changing it, verify the existing version without Helm registry
credentials:

```sh
HELM_REGISTRY_CONFIG="$(mktemp)" helm pull \
  oci://ghcr.io/mcsrobert/charts/openbao-rotation-exporter \
  --version <version> --destination /tmp
```

Then re-run only the failed jobs of the original release workflow run. Do not
re-run the successful publishing job or move the tag: the workflow refuses to
overwrite published versions.

## License

[Apache-2.0](./LICENSE)
