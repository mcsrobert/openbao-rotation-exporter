# AGENTS.md

A Prometheus exporter for OpenBao KV v2 rotation deadlines. The
[README](./README.md) is the contract: its configuration, metric names and
failure behavior are public API, so change them deliberately and keep the
README, the chart and its tests in step.

## Layout

```text
cmd/openbao-rotation-exporter/  Entry point
internal/config/                Configuration file parsing and validation
internal/openbao/               Login and KV v2 metadata client
internal/collector/             Background loop and immutable snapshots
internal/metrics/               Prometheus exposition of a snapshot
internal/server/                HTTP endpoints
charts/openbao-rotation-exporter/  Helm chart with the alert rules
test/integration/               Tests against a real OpenBao dev server
test/chart/                     Chart rendering and alert rule tests
test/e2e/                       kind test of the chart with Kubernetes auth
examples/                       OpenBao policy
```

## Rules

- Never call a KV `data` path or otherwise read secret values. The client only
  needs the login and metadata endpoints.
- Never put tokens, JWTs or OpenBao response bodies in errors or logs.
- HTTP endpoints serve the latest snapshot. They must not contact OpenBao.
- Label values must stay bounded: configured keys and the reasons in
  `internal/openbao/errors.go`.
- A new configuration field also goes in the chart's `values.schema.json`, which
  rejects unknown fields.
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
mise run test:chart
mise run test:image    # needs Docker
mise run test:e2e      # needs Docker
```

## Git

- Commit messages are conventional commits with a scope, such as
  `fix(collector): keep the last due time`. A hook rejects anything else.
- Changes reach `main` through a pull request, with the `ci` check passing.
- Commit types decide releases: `feat` and `fix` create one, so use another type
  for changes that do not affect the image or chart.
- Releases come from merging the pull request release-please keeps open. Do not
  push tags by hand: the release workflow expects the GitHub release to exist.
- The `vX.Y.Z` tag publishes an immutable image and chart, both versioned
  `X.Y.Z`. The chart in git has a placeholder version, and the release sets its
  version and pins the image digest.
- Do not edit `CHANGELOG.md` or `.release-please-manifest.json`. Release-please
  maintains them.
