# Changelog

All notable changes to Owl. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [semantic versioning](https://semver.org).

## [Unreleased]

## [0.7.0] - 2026-10-27

### Added

- History is kept for 60 days and survives relay restarts and upgrades: every 15 s for 6 hours, then 5-minute averages for 7 days and hourly averages for 60 days, each with its peak. The relay saves it to a volume (`relay.persistence`, 100 Mi by default): from the default StorageClass, a named one, a PersistentVolume you created, or one the chart creates on a node folder or NFS share.
- Overview: 24 h, 7 d and 60 d ranges; hovering the chart shows the peak of each period.

### Fixed

- Sign-in page: the owl's eyes move smoothly again (the transition was only applied with reduced motion).

## [0.6.0] - 2026-10-27

### Added

- Overview, the new first section of each cluster: CPU, memory, running pods and open problems with trends, a CPU and memory chart over the last hour or 6 hours, the top pods by CPU and by memory, pods per namespace and the latest problems. The relay keeps 6 hours of history in memory.
- Sidebar: each cluster shows its open problems and CPU and memory bars; the sections of the open cluster (Overview, Problems, Pods, Explore, Usage) with counts; "Watch another cluster" with the commands to run; theme switch (system, light, dark), project link and relay version.
- Pod tables: click a column header to sort.
- New sign-in page: the owl blinks and watches the pointer and the token field, the token can be shown before signing in, and "Where is my token?" gives the command to get it.

### Changed

- Restarts show when the last one happened, like kubectl ("15 (8h ago)"), and are highlighted only when it was within the last hour.
- Finished pods show as Completed and no longer count as running; the header shows running pods out of the pods that should be running.
- Upgrade the relay and the agents together (the chart does): a 0.6.0 agent sends fields an older relay rejects.

## [0.5.0] - 2026-10-26

### Added

- Download the whole log of a container, and the manifest of any resource as YAML or JSON. Manifests are shown as YAML by default.

## [0.4.0] - 2026-10-26

### Added

- Usage tab: live CPU and memory per node, workload and pod against capacity, requests and limits (from metrics-server), and cluster CPU and memory in the header.
- Live pod logs: new lines appear every second, with Live/Paused to stop following and read.

## [0.3.0] - 2026-10-26

First public release.

### Added

- Web app: sign-in with the relay token, cluster list with health, Problems, Pods and Explore views, pod logs and JSON view, dark mode, mobile layout.
- Relay: serves the web app and the API on port 2300 (HTTP) and 8443 (HTTPS, published as 443 by the chart), reloads renewed certificates without a restart.
- Agent: watches pods, nodes and warning events; answers resource, object and log requests; works in-cluster or from a kubeconfig with an optional context.
- Helm chart: relay plus one agent per cluster, generated tokens kept across upgrades, TLS self-signed, from a Secret or from cert-manager, optional Ingress, read-only RBAC without Secrets.
- Multi-architecture images: linux/amd64 and linux/arm64.