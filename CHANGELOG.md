# Changelog

## [0.2.1](https://github.com/mcsrobert/openbao-rotation-exporter/compare/v0.2.0...v0.2.1) (2026-11-11)


### Bug Fixes

* **alerts:** avoid false failures during exporter startup ([d225eb9](https://github.com/mcsrobert/openbao-rotation-exporter/commit/d225eb9d6ca7055bbfc4a62ffa4989369e0ec44b))

## [0.2.0](https://github.com/mcsrobert/openbao-rotation-exporter/compare/v0.1.0...v0.2.0) (2026-11-09)


### Features

* **chart:** publish a Helm chart to GHCR ([c49c29c](https://github.com/mcsrobert/openbao-rotation-exporter/commit/c49c29cb5d926025d9eea1b1a1d54c9a1a0f2526))
* **chart:** publish a Helm chart to GHCR ([013a4a7](https://github.com/mcsrobert/openbao-rotation-exporter/commit/013a4a79950ca293b05b95ad1f1f96e7c2d79ebb))
* **exporter:** export the configured collection interval ([9e63b22](https://github.com/mcsrobert/openbao-rotation-exporter/commit/9e63b22506cd7927d60a76b5e4d0cf4929ce055e))


### Bug Fixes

* **chart:** keep rotation alerts firing across pod rollouts ([1f027b9](https://github.com/mcsrobert/openbao-rotation-exporter/commit/1f027b9615b43b3f57115c23ba56c37254d9c0ee))
* **chart:** keep rotation alerts firing across pod rollouts ([025bbd8](https://github.com/mcsrobert/openbao-rotation-exporter/commit/025bbd879f479db17fdffc8eb0a465d838651b59))
* **chart:** scale the stale collection alert with the interval ([753ecc7](https://github.com/mcsrobert/openbao-rotation-exporter/commit/753ecc7f9d4e8a141bf59d93226dfc4acb591946))
* **chart:** scale the stale collection alert with the interval ([6dfcadc](https://github.com/mcsrobert/openbao-rotation-exporter/commit/6dfcadc3a13d8fa584d0215c5ade347b03563905))
* **config:** default the collection timeout to at most the interval ([f54aded](https://github.com/mcsrobert/openbao-rotation-exporter/commit/f54adedc7d2e80eac2563868f0c482b7a723be9a))
* **config:** default the collection timeout to at most the interval ([f8d9ab0](https://github.com/mcsrobert/openbao-rotation-exporter/commit/f8d9ab0588e1342e21ef53352de5798d73a0b3d3))
* **exporter:** address configuration and logging review findings ([ea39c75](https://github.com/mcsrobert/openbao-rotation-exporter/commit/ea39c75a57baabea89c61f214cb65d5d69a2fa1f))
* **openbao:** revoke login tokens after each collection ([23ae697](https://github.com/mcsrobert/openbao-rotation-exporter/commit/23ae69786922cb8cee1134df498c2f0f4244724d))
* **openbao:** revoke login tokens after each collection ([c3edee1](https://github.com/mcsrobert/openbao-rotation-exporter/commit/c3edee117bc4bcb94d75bb69fb505bfe02cce4db))
