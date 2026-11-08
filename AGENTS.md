# AGENTS.md

A Prometheus exporter for OpenBao KV v2 rotation deadlines. The
[README](./README.md) is the contract: its configuration, metric names and
failure behavior are public API, so change them deliberately and keep the
README, `examples/alerts.yaml` and its tests in step.

## Layout

```text
cmd/openbao-rotation-exporter/  Entry point
internal/config/                Configuration file parsing and validation
internal/openbao/               Login and KV v2 metadata client
internal/collector/             Background loop and immutable snapshots
internal/metrics/               Prometheus exposition of a snapshot
internal/server/                HTTP endpoints
test/integration/               Tests against a real OpenBao dev server
test/e2e/                       kind test with Kubernetes auth
examples/                       Policy, alert rules and manifests
```

## Rules

- Never call a KV `data` path or otherwise read secret values. The client only
  needs the login and metadata endpoints.
- Never put tokens, JWTs or OpenBao response bodies in errors or logs.
- HTTP endpoints serve the latest snapshot. They must not contact OpenBao.
- Label values must stay bounded: configured keys and the reasons in
  `internal/openbao/errors.go`.
- Do not bump dependency, image or action versions by hand. Renovate manages
  them.
- Do not add explanatory comments to workflows or configuration files. Keep
  functional ones, such as Renovate annotations and action version comments.

## Validation

Run `mise run setup` once per checkout.

```sh
mise run lint
mise run test
mise run test:integration
mise run test:rules
mise run test:image    # needs Docker
mise run test:e2e      # needs Docker
```

## Git

- Commit messages are conventional commits with a scope, such as
  `fix(collector): keep the last due time`. A hook rejects anything else.
- Changes reach `main` through a pull request, with the `ci` check passing.
- Releases are `vX.Y.Z` tags. They publish an immutable image tagged `X.Y.Z`.
