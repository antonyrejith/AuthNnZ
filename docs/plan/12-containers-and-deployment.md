# 12 — Containers & Deployment

Every component ships as a signed, minimal container image. The same images
run in local development, CI, our SaaS cells, dedicated cells, customer clouds
and air-gapped data centers.

## 1. Container images

### 1.1 Image rules

| Rule | How |
|------|-----|
| One image per deployable | `idp-runtime`, `pdp`, `directory-api`, `mgmt-api`, `worker`, `ext-runtime`, `router`, `edge-pdp`, `connector-agent`, plus the all-in-one `portun` and the static `console`/`login` assets |
| Minimal base | Distroless static (Debian 13, `nonroot`): no shell, no package manager, CA certificates and time zones only |
| Static binaries | `CGO_ENABLED=0`, `-trimpath`, version stamped at build time |
| Multi-architecture | `linux/amd64` and `linux/arm64` from one build (Buildx) |
| Non-root and read-only | UID 65532, read-only root filesystem, all capabilities dropped |
| FIPS variant | `-fips` images built with the Go FIPS 140-3 module enabled (`GOFIPS140`) |
| Pinned | Every image reference, including base images, build images and the Dockerfile frontend, uses an exact tag **and** digest; changed only by a reviewed upgrade pull request ([08 §1](08-engineering-standards.md#1-version-policy-pinned-everywhere-upgrades-on-purpose)) |
| Supply chain | SBOM (SPDX) and SLSA provenance attached; signed with Cosign (keyless); scanned with Trivy; critical or high vulnerabilities with a fix available block the release |
| Tags | Semantic version and git SHA; deployments always reference the **digest** |

### 1.2 Dockerfile

One Dockerfile builds every Go deployable; `SERVICE` selects which.

```dockerfile
# syntax=docker/dockerfile:1.29.0@sha256:<digest>

FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:<digest> AS build
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    go mod download
ARG TARGETOS TARGETARCH SERVICE VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=bind,target=. \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian13:nonroot@sha256:<digest>
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
```

### 1.3 Runtime contract (every service)

- **Ports:** `8080` for traffic (HTTP/gRPC); `9090` admin port (health,
  metrics, `pprof` behind authentication), never exposed outside the cluster.
- **Health:** `/healthz/startup`, `/healthz/live` (process is healthy) and
  `/healthz/ready` (dependencies reachable, runtime bundles loaded).
- **Configuration:** environment variables and mounted files, validated at
  startup (fail fast with a clear message). Secrets from mounted files provided
  by the secret manager, never baked into images.
- **Shutdown:** on `SIGTERM`, stop accepting work, drain in-flight requests
  within 25 seconds, flush telemetry, exit.
- **Telemetry:** OTLP traces, metrics and logs to the OpenTelemetry Collector;
  JSON logs on stdout.
- **Memory and CPU:** `GOMEMLIMIT` set to about 90% of the memory limit. Go
  sizes `GOMAXPROCS` from the container's CPU limit automatically.

## 2. Local development

`make dev-up` starts everything with Docker Compose; `make dev-k8s` runs the
Helm chart in a local kind cluster for chart and operator work.

```yaml
# deploy/compose/compose.yaml (excerpt)
name: portun
services:
  postgres:
    image: postgres:19.0@sha256:<digest>
    environment:
      POSTGRES_PASSWORD: dev-only-password
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 2s
  valkey:
    image: valkey/valkey:9.2.0@sha256:<digest>
  nats:
    image: nats:2.15.3@sha256:<digest>
    command: ["--jetstream"]
  mailpit:                          # captures every email; UI on :8025
    image: axllent/mailpit:v1.34.0@sha256:<digest>
    ports: ["8025:8025"]
  otel:                             # Grafana, Tempo, Loki, Prometheus in one container; UI on :3000
    image: grafana/otel-lgtm:0.41.1@sha256:<digest>
    ports: ["3000:3000"]
  portun:                          # all-in-one binary
    build:
      context: ../..
      dockerfile: deploy/docker/Dockerfile
      args: { SERVICE: portun }
    depends_on:
      postgres: { condition: service_healthy }
    environment:
      PORTUN_DATABASE_URL: postgres://postgres:dev-only-password@postgres:5432/postgres
      PORTUN_CACHE_URL: redis://valkey:6379
      PORTUN_BUS_URL: nats://nats:4222
      PORTUN_SMTP_URL: smtp://mailpit:1025
      OTEL_EXPORTER_OTLP_ENDPOINT: http://otel:4317
    ports: ["8080:8080"]
  clickhouse:
    image: clickhouse/clickhouse-server:26.10.9.12@sha256:<digest>
    profiles: ["full"]              # analytics and audit search
```

`<digest>` stands for each image's exact SHA-256 digest, recorded when the
file is created; the pin check rejects any image without one. Profiles: the default profile
runs the core stack; `full` adds ClickHouse, S3-compatible storage and
containerized test identity providers (SAML and OIDC) for federation work.

## 3. Kubernetes

### 3.1 Helm chart

```
deploy/helm/portun/
├── Chart.yaml
├── values.yaml
├── values.schema.json          # validates every value; bad values fail at install time
└── templates/
    ├── _helpers.tpl
    ├── idp-runtime/   deployment, service, hpa, pdb, networkpolicy, servicemonitor
    ├── pdp/           …
    ├── directory-api/ …
    ├── mgmt-api/      …
    ├── worker/        deployment, keda-scaledobject, …
    ├── ext-runtime/   deployment (dedicated node pool), …
    ├── migrations/    job (pre-upgrade hook)
    └── gateway/       httproute, grpcroute, certificates
```

The chart is published to an OCI registry, signed, and used unchanged by our
SaaS, dedicated cells and self-hosted customers; only values differ.

### 3.2 Pod security (every workload)

```yaml
spec:
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    seccompProfile: { type: RuntimeDefault }
  topologySpreadConstraints:
    - maxSkew: 1
      topologyKey: topology.kubernetes.io/zone
      whenUnsatisfiable: DoNotSchedule
      labelSelector: { matchLabels: { app.kubernetes.io/name: idp-runtime } }
  containers:
    - name: idp-runtime
      image: registry.example.com/portun/idp-runtime@sha256:<digest>
      ports:
        - { name: http, containerPort: 8080 }
        - { name: admin, containerPort: 9090 }
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: { drop: ["ALL"] }
      resources:
        requests: { cpu: 500m, memory: 512Mi }
        limits: { cpu: "2", memory: 512Mi }
      env:
        - name: GOMEMLIMIT
          value: "460MiB"             # about 90% of the memory limit; computed by the chart
      startupProbe:   { httpGet: { path: /healthz/startup, port: admin } }
      livenessProbe:  { httpGet: { path: /healthz/live,    port: admin } }
      readinessProbe: { httpGet: { path: /healthz/ready,   port: admin } }
```

### 3.3 Platform practices

| Concern | Practice |
|---------|----------|
| Admission | Pod Security Standards `restricted`; Kyverno policies allow only signed images from our registry and require limits, probes and labels |
| Network | Default-deny NetworkPolicies; explicit allow rules per service; egress from `ext-runtime` and `worker` only through the egress proxy |
| Ingress | Gateway API (`HTTPRoute`, `GRPCRoute`) with Envoy Gateway or the cloud's implementation; TLS through cert-manager, including on-demand certificates for customer custom domains |
| Service-to-service | mTLS (service mesh or SPIFFE-based; decided during foundations, D-19) |
| Availability | ≥ 3 replicas across zones, PodDisruptionBudgets, graceful shutdown, readiness gates |
| Scaling | HPA on CPU and request rate for `idp-runtime`, `pdp` and `directory-api`; KEDA on queue depth for `worker`; node autoscaling (Karpenter or the cloud equivalent) |
| Connectivity | Static egress IPs per region; private endpoints through AWS PrivateLink, Azure Private Link and Google Private Service Connect (OPS-07) |
| Secrets | External Secrets Operator syncing from the cloud secret manager or Vault; no secrets in Git or Helm values |
| Migrations | A Job runs migrations before rollout. Migrations are backward-compatible, so old and new pods run side by side |
| Isolation | `ext-runtime` runs on a dedicated, tainted node pool; dedicated cells get their own cluster |
| Observability | OpenTelemetry Collector; Prometheus, Loki and Tempo (or the customer's stack); dashboards and alerts stored as code |
| Versions | Exact chart versions with `Chart.lock`; Argo CD `targetRevision` pinned to an exact version or commit SHA; images by digest only (Kyverno rejects others); cluster add-ons upgraded only through reviewed pull requests |
| Audit | Kubernetes API audit policy enabled; pgAudit for human database sessions; audit archive in write-once object storage; platform audit trail shipped to a separate security account ([13 §7](13-audit-and-change-tracking.md#7-platform-and-infrastructure-audit-our-side)) |

## 4. Build and release pipeline

```mermaid
flowchart LR
    PR[Pull request] --> CI[Lint, tests,<br/>security scans]
    CI --> MERGE[Merge to main]
    MERGE --> BUILD[Build multi-arch images]
    BUILD --> ATTEST[SBOM + provenance<br/>Cosign signature]
    ATTEST --> SCAN{Trivy scan<br/>passes?}
    SCAN -- no --> STOP[Block release]
    SCAN -- yes --> REG[Push to registry]
    REG --> GITOPS[Update image digests<br/>in GitOps repo]
    GITOPS --> DEV[Argo CD: dev<br/>E2E + conformance]
    DEV --> STG[Staging canary<br/>+ load and chaos]
    STG --> CANARY[Production: canary cell]
    CANARY --> WAVES[Remaining cells in waves<br/>10% → 50% → 100%]
    WAVES -. SLO burn or error spike .-> RB[Automatic rollback]
    CANARY -. SLO burn or error spike .-> RB
```

- **GitOps:** Argo CD deploys whatever the GitOps repository declares; nobody
  runs `kubectl apply` against production.
- **Progressive delivery:** Argo Rollouts canaries each deployment and analyzes
  SLO metrics (error rate, latency, login success) before promoting.
- **Cells are deployed in waves**, so a bad release reaches a small share of
  tenants before it is rolled back.
- **Database changes** ship ahead of the code that needs them (expand),
  and cleanup ships after all cells run the new code (contract).

## 5. Environments

| Environment | Purpose | Lifetime |
|-------------|---------|----------|
| Local | Docker Compose or kind on a laptop | On demand |
| Preview | Full stack per pull request in an ephemeral namespace | Deleted when the PR closes |
| Dev | Latest `main`; integration with real cloud services | Permanent |
| Staging | Production-shaped cell; canary, load and chaos testing | Permanent |
| Performance | Production-sized cell for load and soak tests | Scheduled |
| Sandbox | Customer test tenants (free and paid) | Permanent |
| Production | Cells per region; dedicated cells per enterprise customer | Permanent |

## 6. Cloud infrastructure (SaaS)

OpenTofu modules create each region and cell on **AWS, Azure and Google Cloud
from day one** (D-07): network, Kubernetes cluster (EKS, AKS or GKE), managed
PostgreSQL (multi-AZ), managed Valkey, KMS keys (AWS KMS, Azure Key Vault,
Google Cloud KMS), object storage, CDN and WAF, and DNS. Staging and
performance cells run on all three clouds, so every release is tested on each. OpenTofu, its providers, and every
engine, cluster and node-image version are pinned exactly; automatic minor
upgrades and auto-upgrading release channels are turned off, and a policy check
on every plan rejects anything unpinned. A new cell is one module instance plus a
GitOps entry. Infrastructure changes go through the same PR review, plan output
and policy scanning as application code.

## 7. Self-hosted packaging

| Package | For |
|---------|-----|
| **All-in-one image** + Compose bundle | Evaluation, development and small installations |
| **Helm chart** (OCI) | Each chart version pins exact image digests, so a customer's upgrade is always an explicit choice of chart version. Production on any conformant Kubernetes: the last two upstream minor versions, EKS, AKS, GKE and OpenShift (restricted SCC, arbitrary UIDs) |
| **Air-gapped bundle** | Images, chart, SBOMs and signatures as OCI artifacts that can be mirrored into an offline registry; offline license; FIDO metadata and breached-password data bundled; no outbound calls required |
| **Kubernetes operator** | Day-2 operations: upgrades, backups, key rotation, scaling |

Self-hosted customers bring PostgreSQL (managed or an operator such as
CloudNativePG), Valkey, object storage and a KMS or HSM (Vault, PKCS#11 or a
cloud KMS). NATS ships inside the chart.
