# Changelog

All notable changes to Owl. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [semantic versioning](https://semver.org).

## [Unreleased]
- Live pod logs: new lines appear every 2 seconds, with Live/Paused to stop following and read.
### Added

- Usage tab: live CPU and memory per node, workload and pod against capacity, requests and limits (from metrics-server), and cluster CPU and memory in the header.
## [0.3.0] - 2026-10-26

First public release.

### Added

- Web app: sign-in with the relay token, cluster list with health, Problems, Pods and Explore views, pod logs and JSON view, dark mode, mobile layout.
- Relay: serves the web app and the API on port 2300 (HTTP) and 8443 (HTTPS, published as 443 by the chart), reloads renewed certificates without a restart.
- Agent: watches pods, nodes and warning events; answers resource, object and log requests; works in-cluster or from a kubeconfig with an optional context.
- Helm chart: relay plus one agent per cluster, generated tokens kept across upgrades, TLS self-signed, from a Secret or from cert-manager, optional Ingress, read-only RBAC without Secrets.
- Multi-architecture images: linux/amd64 and linux/arm64.
