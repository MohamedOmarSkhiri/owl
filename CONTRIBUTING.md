# Contributing to Owl

## Requirements

- Go 1.27+
- Node.js 22+
- Docker, Helm 3.14+ or 4, kubectl
- A cluster to test against (K3s, kind or minikube)

## Run it locally

Three terminals, from the repository root.

Relay (HTTP on 2300):

```bash
cd relay
go build -o bin/relay ./cmd/relay
export RELAY_AGENT_TOKEN=agent-dev-123 RELAY_APP_TOKEN=app-dev-123
./bin/relay
```

Agent (uses your current kubectl context):

```bash
cd agent
go build -o bin/agent ./cmd/agent
export AGENT_TOKEN=agent-dev-123
./bin/agent --cluster dev
```

Web app with live reload (forwards `/v1` to the relay on 2300):

```bash
cd web
npm ci
npm run dev
```

Open http://localhost:5173 and sign in with `app-dev-123`.

## Before you open a pull request

Run the same checks as CI:

```bash
cd relay && gofmt -l . && go vet ./... && go test -race ./... && cd ..
cd agent && gofmt -l . && go vet ./... && go test -race ./... && cd ..
cd web && npm run build && cd ..
helm lint charts/owl
```

`gofmt -l .` must print nothing. CI also installs the chart on a kind cluster and checks that the agent reports and the relay answers live requests.

## Commit messages

One line, prefixed with the area: `relay: ...`, `agent: ...`, `web: ...`, `chart: ...`, `ci: ...`, `docs: ...`.

## Releasing (maintainers)

One tag releases everything: both images (amd64 and arm64), the Helm chart and the GitHub Release.

1. Make sure `main` is green in CI.
2. Pick the version with [semantic versioning](https://semver.org): bug fix 0.3.1, new feature 0.4.0, breaking change 1.0.0.
3. Bump `version` and `appVersion` in `charts/owl/Chart.yaml` to that version.
4. Move the `Unreleased` notes in `CHANGELOG.md` under a new `## [0.4.0] - YYYY-MM-DD` heading.
5. Commit, tag and push:

   ```bash
   git add charts/owl/Chart.yaml CHANGELOG.md
   git commit -m "release: v0.4.0"
   git tag v0.4.0
   git push origin main v0.4.0
   ```

6. Watch the `release` workflow in the Actions tab. When it is green:

   ```bash
   helm show chart oci://ghcr.io/<owner>/charts/owl --version 0.4.0
   ```

The workflow refuses to release when the tag does not match `Chart.yaml` or when `CHANGELOG.md` has no section for the version.
