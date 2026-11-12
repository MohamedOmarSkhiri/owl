# Changelog

All notable changes to Owl. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [semantic versioning](https://semver.org).

## [Unreleased]

## [0.13.0] - 2026-11-12

### Added

- Certificate expiry. Every 10 minutes the agent checks the certificates that keep the cluster and its apps reachable, and raises `CertificateExpiring` 14 days before one runs out (critical at 3 days, and once expired):
  - cert-manager Certificates, from their status. `CertificateNotReady` when cert-manager cannot issue or renew one for 10 minutes, with its reason.
  - The HTTPS hosts of Ingresses and HTTPRoutes that cert-manager does not manage: the agent reads the certificate each host serves, as a browser would. A host the agent cannot reach is skipped, never reported, and a certificate that does not cover the host (a default one) is ignored.
  - The API server's certificate and each node's kubelet certificate.
  - The client certificate in the kubeconfig an agent signs in with, 30 days ahead (critical at 7): when it expires, Owl loses that cluster.
  - Owl's own HTTPS certificate (the chart points the local agent at it when `tls.enabled`).

  No Secret is read. Wildcard hosts are not checked: they have no single address to call. The agent logs what it watches after the first check.
- Certificates view, next to Usage: every certificate the agent watches, first to expire on top, with its expiry date, time left and status (OK, expires soon, expiring, not ready, not reachable from the cluster with the reason, or not checked when a host serves another name's certificate). Click one to open its object. API: `GET /v1/clusters/{id}/certificates`.
- Certificate, Ingress and HTTPRoute problems open the object's YAML.

## [0.12.0] - 2026-11-12

### Added

- Mute problems. From the Problems view, mute one problem, every problem with the same reason in its namespace, or a whole namespace, for 1 hour, 1 day, 1 week, or until the problem resolves. Muted problems send no notifications (opening or resolved) and stay listed, greyed out at the bottom, with Unmute.
- Acknowledge a problem: mute it until it resolves, with a note ("known, fixing Monday") shown under it. The mute ends by itself when the problem resolves.
- Mutes wider than one problem are listed at the top of Problems, so none is forgotten.
- Mutes are kept in `/data/mutes.json` next to the history and survive restarts and upgrades.
- API: `GET`, `POST /v1/clusters/{id}/mutes` and `DELETE /v1/clusters/{id}/mutes/{muteId}`.

### Changed

- The problem count in the sidebar and the Overview no longer includes muted problems; `GET /v1/clusters` gives them as `problemsMuted`. Cluster health still takes every problem into account.

## [0.11.0] - 2026-11-12

### Added

- Problems beyond pods. Owl now also reports:
  - nodes that are not Ready for a minute, or under memory, disk or PID pressure;
  - Deployments whose rollout is stuck (`progressDeadlineSeconds` passed);
  - Deployments, StatefulSets and DaemonSets with fewer ready replicas than wanted for 10 minutes (critical when none is ready);
  - Jobs that failed in the last 24 hours, and CronJobs whose last run failed (cleared when a later run succeeds);
  - PersistentVolumeClaims that lost their volume, or stay Pending for 5 minutes although they should be provisioned. Claims of a `WaitForFirstConsumer` class, like k3s local-path, are only flagged once a pod needs them.

  They show in the Problems view and trigger notifications like the others, and close by themselves when fixed.
- Click any problem about a node, workload, Job or volume to open its YAML (pods still open with their logs).

## [0.10.2] - 2026-11-12

### Fixed

- The map is live for real: the relay now forwards the app's `since` to the agent, so the agent holds the request until something changes. In 0.10.1 the relay dropped it and the map fell back to asking again.
- A crash loop stays one open problem while the container shows "Error" between restarts, instead of closing and opening again at each restart. The problem shows the run that just crashed: its exit code, reason and last log lines.

## [0.10.1] - 2026-11-04

### Changed

- The map is live: it updates about a second after a pod, workload or Service in the namespace changes (the agent holds each request until something changes, instead of the app asking every 15 seconds). The toolbar shows "Live · updated N s ago".
- Pods, ReplicaSets and Jobs show the end of their name on the map, the part that changes when a pod is replaced; the full name is in the tooltip.
- Terminating pods are amber on the map.

### Fixed

- Warning events show when they happened, not when the agent saw them: after a restart of the agent or the node, they no longer all say "2m ago".

## [0.10.0] - 2026-11-02

### Added

- Map: a namespace as a graph, like Argo CD's application tree. Ingresses and HTTPRoutes lead to Services, Services to the Deployments, StatefulSets and DaemonSets they select, then ReplicaSets, Jobs and Pods, with the ConfigMaps, Secrets (names only, never read) and volumes they use. Each box shows its health; failing paths are highlighted; a ConfigMap or Service that is referenced but missing shows as "not found". Click a box to open its logs or manifest. Pan, zoom, and hide pods or config to focus.

### Changed

- Upgrade the relay and the agents together (the chart does): the map needs a 0.10.0 agent.

## [0.9.0] - 2026-11-02

### Added

- Crash recorder: when a container crashes (CrashLoopBackOff, OOMKilled), the agent saves the exit code and the last 100 lines the container printed, before Kubernetes throws that log away at the next restart. The Problems view shows them under the problem, with what the exit code usually means and a download button.
- Crash notifications include the exit code and the last 5 log lines (`notifications.logLines`, 0 to 20; 0 sends the exit code only).

### Changed

- Upgrade the relay and the agents together (the chart does): a 0.9.0 agent sends crash reports an older relay rejects.

## [0.8.1] - 2026-10-31

### Fixed

- The relay answers `/v1/info` again (lost in 0.8.0): the version in the sidebar footer, and the Overview's note on whether history survives restarts.
- README: `--reset-then-reuse-values` typo, and the feature list now includes Overview, Notifications and Usage.

## [0.8.0] - 2026-10-27

### Added

- Notifications: a message on Telegram, Slack, Discord or any webhook when a problem opens, resolves or comes back, and when a cluster goes down or comes back. Critical problems only by default (`notifications.minSeverity`), at most one message per problem every 10 minutes (`notifications.cooldown`), with a link to the problem when `notifications.publicUrl` is set. Tokens are kept in a Secret, or your own (`notifications.existingSecret`).
- Notifications panel (bell at the bottom of the sidebar): which channels are on, what is sent, a test button, and how to set up Telegram.

### Fixed

- The Overview no longer says the history is kept for 60 days when the relay has no volume (`relay.persistence.enabled=false`).
- Upgrade commands in the docs and the app use `--reset-then-reuse-values`: with `--reuse-values`, an upgrade does not pick up settings added by the new chart version (such as `relay.persistence` in 0.7.0).

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