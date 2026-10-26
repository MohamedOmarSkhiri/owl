# Owl Helm chart

Installs the Owl relay (web app + API) and one read-only agent per watched cluster.

```bash
helm install owl oci://ghcr.io/mohamedomarskhiri/charts/owl -n owl --create-namespace
```

## Ports

| Service port | Protocol | Pod port | Notes |
|---|---|---|---|
| 2300 | HTTP | 2300 | Always on |
| 443 | HTTPS | 8443 | When `tls.enabled` (default). The pod uses 8443 because it runs as non-root |

With `service.type=LoadBalancer` on K3s, ServiceLB opens these ports on every node. Check they are free first: `kubectl get svc -A | grep -E '443|2300'`. Traefik already uses 443 on a default K3s install; either free it or keep Owl as `ClusterIP` and set `ingress.enabled=true`.

## Watching clusters

Each entry in `clusters` runs one agent.

| Entry | Watches | Credentials |
|---|---|---|
| `{name: local}` (default) | The cluster Owl is installed in | Read-only ServiceAccount created by the chart |
| `{name: prod, kubeconfig: ...}` | The cluster in that kubeconfig | Stored by the chart in a Secret |
| `{name: prod, existingSecret: my-kc}` | The cluster in your Secret (key `config`) | Your Secret |
| add `context: name` | That context inside the kubeconfig | |

Kubeconfigs must embed their credentials. Exec plugins (`aws`, `gke-gcloud-auth-plugin`, `kubelogin`) cannot run in the agent image.

```bash
helm upgrade --install owl oci://ghcr.io/mohamedomarskhiri/charts/owl -n owl --reuse-values \
  --set 'clusters[0].name=local' \
  --set 'clusters[1].name=prod' --set-file 'clusters[1].kubeconfig=./prod.yaml'
```

## Values

| Key | Default | Description |
|---|---|---|
| `image.relay.repository` | `ghcr.io/mohamedomarskhiri/owl-relay` | Relay image (includes the web app) |
| `image.relay.tag` | chart `appVersion` | |
| `image.agent.repository` | `ghcr.io/mohamedomarskhiri/owl-agent` | Agent image |
| `image.agent.tag` | chart `appVersion` | |
| `imagePullSecrets` | `[]` | For private registries |
| `auth.existingSecret` | `""` | Secret with keys `app-token` and `agent-token` |
| `auth.appToken`, `auth.agentToken` | `""` | Empty: random tokens generated once and kept |
| `service.type` | `ClusterIP` | `ClusterIP`, `NodePort` or `LoadBalancer` |
| `service.httpPort` | `2300` | |
| `service.httpsPort` | `443` | |
| `service.nodePorts.http`, `.https` | `""` | NodePort numbers (30000-32767) |
| `service.loadBalancerIP` | `""` | |
| `service.annotations` | `{}` | |
| `tls.enabled` | `true` | Serve HTTPS from the relay |
| `tls.mode` | `selfSigned` | `selfSigned`, `secret` or `certManager` |
| `tls.hosts` | `[]` | DNS names and IPs in the certificate |
| `tls.secretName` | `""` | `mode=secret`: existing `kubernetes.io/tls` Secret |
| `tls.certManager.issuerRef` | `ClusterIssuer/letsencrypt-prod` | `mode=certManager` |
| `ingress.enabled` | `false` | Also publish through an Ingress controller (HTTP backend on 2300) |
| `ingress.className`, `.host`, `.annotations`, `.tls.secretName` | | |
| `clusters` | `[{name: local}]` | See above |
| `rbac.create` | `true` | Read-only ClusterRole for the in-cluster agent |
| `rbac.extraApiGroups` | common CRD groups | CRD groups the agent may read |
| `relay.resources`, `.nodeSelector`, `.tolerations`, `.affinity` | | |
| `agent.resources`, `.nodeSelector`, `.tolerations`, `.affinity` | | |

## Notes

- The relay keeps state in memory and runs one replica. After a restart, agents repopulate it within a minute.
- The tokens Secret has `helm.sh/resource-policy: keep`, so a reinstall keeps your tokens. Delete it to rotate them.
- The self-signed certificate is generated once. To regenerate it after changing `tls.hosts`, delete the `owl-tls` Secret and run `helm upgrade`.
