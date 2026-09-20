# Codex Ticket direct-exit compatibility build

The bundled `v0.2.0-ccpa.1` is derived from
[Su-cyber-art/cpa-plugin-codex-ticket v0.2.0](https://github.com/Su-cyber-art/cpa-plugin-codex-ticket/tree/v0.2.0),
commit `482ade49bd169f0ef05fec67f297ce636c6fa7e9` (MIT).
The build script verifies the source archive SHA256, applies `direct.patch`,
runs the upstream suite plus the direct-exit test, and copies all upstream notices.

The patch adds an explicit `direct` value to the private exit-selection file.
It never falls back to direct on absent/invalid proxy settings and ignores
ambient HTTP proxy variables for the direct mode. No account/cache/injection
logic changes. The plugin reports the distinct compatibility version.

The control image builds and includes this artifact via the normal GitHub CI
release pipeline. It is not loaded into Admin: it is installed into selected CPA
containers only. Currently linux/amd64 with glibc is supported. Installation
validates the native library against the account's exact image in an isolated
container before touching live configuration.

The configuration center selects `account` or `direct` as the harvesting exit.
Upstream binary releases can also be installed using their exact version, but
explicit direct mode requires the bundled compatibility build. Upgrading the
control image makes its newer bundled plugin available; installation into an
account remains a separate manual action. The scheduled task checks upstream
CPA and plugin release metadata only.

This is an experimental HTTP/SSE workaround. Existing client turn state is
preserved. Cache keys include the credential identity and model; tickets are
memory-only. A 292-character header is a candidate, not proof of model quality.
WebSocket behavior is unchanged. Harvesting creates additional upstream requests.
