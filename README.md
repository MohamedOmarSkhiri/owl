<p align="center"><img src="web/public/owl.svg" width="96" alt=""></p>

<h1 align="center">Owl</h1>

<p align="center">Watch your Kubernetes clusters from the browser: what is broken, every pod and its logs, every resource. Read-only by design.</p>

---

Owl is a small, self-hosted dashboard for Kubernetes and K3s. Install it with Helm, open it on port 2300 (HTTP) or 443 (HTTPS), and see:

- **Overview**: CPU, memory, pods and open problems at a glance, with 60 days of history (1 h to 60 d charts, averages and peaks)
- **Problems**: CrashLoopBackOff, ImagePullBackOff, OOMKilled, failed and long-pending pods, warning events
- **Notifications**: Telegram, Slack, Discord or any webhook when a problem opens or a cluster goes down
- **Pods**: status, restarts (and when), age; open any pod for live logs, download them, or read its YAML
- **Usage**: CPU and memory per node, workload and pod against requests, limits and capacity
- **Explore**: any resource type in any namespace, CRDs included (HTTPRoute, Certificate, Application…), as YAML or JSON
- **Many clusters in one place**: the cluster Owl runs in, plus any other cluster you give it a kubeconfig for

Owl never changes your clusters and never reads Secrets.

## How it works

```
 browser ──2300 / 443──▶ relay (web app + API) ◀── agent ──▶ this cluster (read-only ServiceAccount)
                                ▲
                                └──────────────── agent ──▶ another cluster (kubeconfig you provide)
```

- **relay**: serves the web app and the API, checks the sign-in token, keeps the 60-day history on a volume and sends notifications.
- **agent**: one per cluster. Watches pods, nodes and events, reports problems and CPU and memory, and answers live requests (resource lists, logs). It only makes outbound calls to the relay.

## Install

```bash
helm install owl oci://ghcr.io/mohamedomarskhiri/charts/owl -n owl --create-namespace
kubectl -n owl get secret owl-tokens -o jsonpath='{.data.app-token}' | base64 -d; echo
kubectl -n owl port-forward svc/owl-relay 2300:2300
```

Open http://localhost:2300 and sign in with the token. To publish it on the node instead (ports 2300 and 443 must be free on the node):

```bash
helm upgrade owl oci://ghcr.io/mohamedomarskhiri/charts/owl -n owl --reset-then-reuse-values --set service.type=LoadBalancer
```

Watch another cluster too:

```bash
kubectl config view --minify --flatten --context prod > prod.yaml
helm upgrade owl oci://ghcr.io/mohamedomarskhiri/charts/owl -n owl --reset-then-reuse-values \
  --set 'clusters[0].name=local' --set 'clusters[1].name=prod' --set-file 'clusters[1].kubeconfig=prod.yaml'
```

All options: [charts/owl/README.md](charts/owl/README.md).

## Repository layout

| Folder | What | Language |
|---|---|---|
| `web/` | Web app | React, TypeScript, Vite, Tailwind |
| `relay/` | API server, serves the web app | Go (standard library only) |
| `agent/` | Cluster watcher | Go, client-go |
| `charts/owl/` | Helm chart | Helm |

## Contributing

Bug reports, ideas and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the local setup, tests and release process, and [SECURITY.md](SECURITY.md) to report a vulnerability.

## License

[Apache License 2.0](LICENSE)
