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

## History storage

The relay keeps 60 days of history in `/data/history.json` (about 1.2 MB per cluster). Pick where `/data` lives:

| You have | Values | What happens |
|---|---|---|
| A default StorageClass (K3s `local-path`, cloud disks) | nothing to set | The chart's claim gets a volume automatically |
| Another StorageClass | `relay.persistence.storageClass=longhorn` | That class creates the volume |
| A PersistentVolume you created | `relay.persistence.volumeName=my-pv` | The claim binds to that exact volume |
| A folder on a node, no StorageClass | `relay.persistence.pv.enabled=true`, `pv.hostPath=/var/lib/owl` | The chart creates the PersistentVolume and its claim |
| An NFS share | `relay.persistence.pv.enabled=true`, `pv.nfs.server=…`, `pv.nfs.path=…` | Same, on NFS |
| Your own claim | `relay.persistence.existingClaim=my-claim` | Used as is |
| No storage at all | `relay.persistence.enabled=false` | Memory and `emptyDir`: history is lost when the pod is replaced |

The relay runs as user 65532. Volumes from a StorageClass are made writable automatically (`fsGroup`); a node folder or an NFS export must be writable by that user:

```bash
# on the node (hostPath)
sudo install -d -o 65532 -g 65532 -m 0750 /var/lib/owl
# on the NFS server: chown the export, or squash all users to it
sudo chown 65532:65532 /exports/owl        # or in /etc/exports: (rw,all_squash,anonuid=65532,anongid=65532)
```

If the folder is not writable, the relay stops at start with a message saying so.

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
| `relay.persistence.enabled` | `true` | Keep the 60-day history on a volume across restarts and upgrades |
| `relay.persistence.size` | `100Mi` | About 1.2 MB per watched cluster |
| `relay.persistence.storageClass` | `""` | Empty: the cluster's default (local-path on K3s) |
| `relay.persistence.volumeName` | `""` | Bind to a PersistentVolume you created (no StorageClass needed) |
| `relay.persistence.existingClaim` | `""` | Use your own PersistentVolumeClaim |
| `relay.persistence.pv.enabled` | `false` | The chart creates the PersistentVolume too |
| `relay.persistence.pv.hostPath`, `.node` | `""` | A folder on a node (owned by `65532:65532`) |
| `relay.persistence.pv.nfs.server`, `.path` | `""` | An NFS export writable by uid 65532 |
| `relay.persistence.pv.reclaimPolicy` | `Retain` | Keep the data when the claim is deleted |
| `relay.resources`, `.nodeSelector`, `.tolerations`, `.affinity` | | |
| `agent.resources`, `.nodeSelector`, `.tolerations`, `.affinity` | | |

## Notes

- The relay runs one replica. After a restart, agents report clusters and problems again within a minute, and the history is read back from `/data` (see History storage).
- The tokens Secret has `helm.sh/resource-policy: keep`, so a reinstall keeps your tokens. Delete it to rotate them.
- The self-signed certificate is generated once. To regenerate it after changing `tls.hosts`, delete the `owl-tls` Secret and run `helm upgrade`.