# Upstream Codex Ticket plugin

The default target is the unmodified upstream [Su-cyber-art/cpa-plugin-codex-ticket v0.2.0](https://github.com/Su-cyber-art/cpa-plugin-codex-ticket/releases/tag/v0.2.0).
The installer downloads the original Linux amd64 ZIP from its exact GitHub release,
checks the GitHub asset SHA256, and verifies native compatibility in the account's
exact image before activating it. Control images no longer build or bundle a patched plugin.
Linux amd64 with glibc and a plugin-capable CPA image are required.

Configure the dedicated Ticket proxy in Configuration Center. Its URL, including
optional credentials, is stored in the encrypted secret store and never returned
by the catalog. Blank input preserves the existing value. HTTP, HTTPS, SOCKS5 and
SOCKS5h are accepted. It is projected into a private 0600 proxy file for selected
accounts, separately from the business proxy. The upstream plugin reads that file;
manual edits or its own proxy settings page are overwritten by the next control-plane projection.
Reusing the account proxy remains an explicit alternative. Neither mode falls back to direct.

Legacy target version v0.2.0-ccpa.1 is normalized to v0.2.0. Existing direct settings
become dedicated proxy mode with harvesting and injection disabled until the operator
provides a proxy and explicitly enables them. Existing account-proxy settings remain
account-proxy settings. Installed versions stay pinned; changing the target or
upgrading Control does not install software or start stopped accounts.

Existing account OAuth, plugin paths, host configuration and commercial-mode are
managed by the control plane. Account creation queues a separate automatic installation
job; a failed installation preserves the account and OAuth. Account management shows
installation progress, retries and sanitized live cache/injection observations. Existing
accounts install or upgrade from their account details. Installation enrolls that account
and restarts only the selected running account after compatibility checks; global plugin,
harvest, injection and proxy settings remain authoritative. Legacy account scope is preserved.
This experimental HTTP/SSE plugin does not handle WebSocket, and candidate tickets
do not prove model quality. Harvesting creates additional upstream requests.
