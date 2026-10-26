# Security

## Reporting a vulnerability

Do not open a public issue. Use **Security → Report a vulnerability** on the GitHub repository (private advisory), or email skhiri.mohamedomar@gmail.com. Expect an answer within 7 days.

## Security model

- **Read-only.** The in-cluster agent's ClusterRole grants only `get`, `list` and `watch`. Owl never creates, changes or deletes resources.
- **No Secrets.** Secrets are not in the agent's RBAC, and both the agent and the relay refuse requests for them.
- **Two tokens.** The app token signs people in to the web app; the agent token lets agents report. Neither works on the other's API.
- **Outbound agents.** Agents only call the relay. A watched cluster does not need any open inbound port.
- **Hardened pods.** Non-root, read-only root filesystem, all capabilities dropped, RuntimeDefault seccomp, distroless images. The relay has no Kubernetes credentials at all.

## What a user should know

- Anyone with the app token can read what the agents can read, including pod logs. Treat the token like a password.
- A kubeconfig given to the chart is stored as a Kubernetes Secret in Owl's namespace. Use a read-only ServiceAccount token, never an admin kubeconfig.
- The default HTTPS certificate is self-signed. Use `tls.mode=certManager` or `tls.mode=secret` for a trusted one.
