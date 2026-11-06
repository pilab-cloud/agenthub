# Kubernetes Deployment

## Prerequisites

| Component | Requirements |
| --- | --- |
| **PostgreSQL** | 16+, CNPG recommended, `agenthub` database |
| **Valkey** | Cache/sessions |
| **NATS JetStream** | Optional (M4+), for sync jobs |
| **Object Storage** | S3-compatible (Silo/MinIO) for blobs |
| **DNS** | `agenthub.pilab.hu` A/AAAA record |
| **TLS** | Let's Encrypt certificate (or provided) |

## Kubernetes Manifests

### Namespace

```bash
kubectl apply -f deploy/namespace.yaml
```

### ConfigMap (non-secret config)

```bash
kubectl apply -f deploy/configmap.yaml
```

### Secret (sensitive data)

```bash
kubectl apply -f deploy/secret.yaml
# Contains: DATABASE_DSN, SESSION_SECRET, SSO_CLIENT_ID, SSO_CLIENT_SECRET
```

### PostgreSQL (CNPG)

```bash
kubectl apply -f deploy/k8s/postgres.yaml
# - Cluster: shared-pg or dedicated
# - Database: agenthub
# - User: agenthub (created via kubectl exec)
```

### Migrations

```bash
kubectl apply -f deploy/k8s/migrate-job.yaml
# Then wait for completion:
kubectl -n agenthub wait --for=condition=complete job/agenthub-migrate --timeout=120s
```

### Deployment

```bash
kubectl apply -f deploy/k8s/deployment.yaml
```

### Service

```bash
kubectl apply -f deploy/k8s/service.yaml
```

### Ingress (TLS)

```bash
kubectl apply -f deploy/k8s/ingress.yaml
# Requires:
# - ingress-nginx with cert-manager
# - DNS: agenthub.pilab.hu pointing to ingress controller
# - cert-manager issuer: letsencrypt-prod
```

### Valkey (Cache)

```bash
# Valkey is typically deployed alongside or alongside PostgreSQL
# The config references it via: cache:
#   kind: valkey
#   ...
```

### NATS JetStream (Optional)

```bash
# For sync jobs and event propagation
# Configure in deploy/dev/config.yaml:
#   storage:
#     kind: natjs
```

## Configuration

### `deploy/dev/config.yaml`

```yaml
env: dev

server:
  http_addr: ":8080"
  grpc_addr: ":9090"
  metrics_addr: ":9100"
  public_url: "http://localhost:8080"
  read_timeout: 10s
  write_timeout: 30s
  idle_timeout: 120s
  shutdown_grace: 20s
  max_request_bytes: 4194304
  trusted_proxies: ["127.0.0.1/32"]

database:
  dsn: "postgres://agenthub:agenthub@localhost:5432/agenthub?sslmode=disable"
  max_open_conns: 20
  max_idle_conns: 10
  conn_max_lifetime: 30m
  statement_timeout: 15s
  migrate_on_start: false

storage:
  kind: filesystem
  max_bundle_bytes: 67108864
  max_file_bytes: 10485760

cache:
  kind: memory

auth:
  issuer: "http://localhost:8081"
  audience: agenthub
  audience_allowlist: ["agenthub"]
  client_id: "agenthub-web"
  session_secret: "dev-only-session-secret-change-me-32b"
  redirect_url: "http://localhost:8080/auth/callback"
  post_logout_redirect_url: "http://localhost:8080/"
  scopes: ["openid", "profile", "email", "agenthub.read", "agenthub.publish", "agenthub.admin"]

signing:
  require_publisher_signature: false

observability:
  log_level: info
  log_format: console
  metrics_enabled: true
  pprof_enabled: true

web:
  enable: true
  landing_cards: 12
  landing_ttl: 60s
  allow_web_authoring: true

vault:
  address: "http://localhost:8200"
  path: "secret/data/AgentHub"
  token: "dev-root-token"
```

### Production Config (`deploy/production/`)

Production config differs in:

- `env: prod`
- `public_url: https://agenthub.pilab.hu`
- `database.dsn` points to the production PostgreSQL cluster
- `auth.issuer: https://sso.pilab.hu`
- `auth.session_secret` from HashiCorp Vault or Sealed Secret
- `storage.kind: s3` (or `filesystem` with persistent volume)
- `observability.log_format: json`
- `web.landing_cards: 24`
- Additional TLS and security settings

## Running Locally

```bash
# 1. Start PostgreSQL (Docker)
docker run -d \
  -e POSTINGGRES_DB=agenthub \
  -e POSTINGGRES_USER=agenthub \
  -e POSTINGGRES_PASSWORD=agenthub \
  -p 5432:5432 \
  postgres:16-alpine

# 2. Start Valkey (or use in-memory cache in dev)
# 2. Apply migrations
agenthub migrate --config deploy/dev/config.yaml

# 3. Start the server
agenthub serve --config deploy/dev/config.yaml

# 4. Visit
#    http://localhost:8080
#    http://localhost:8080/healthz -> ok
#    http://localhost:8080/.well-known/agenthub.json -> config
```

## Upgrade Guide

### Upgrading the Server

```bash
# 1. New Docker image built + pushed
# 2. Update deployment image tag
kubectl set image deployment/agenthub-server \
  agenthub-server=ghcr.io/pilab-dev/agenthub-server:v20261106-r1

# 3. Rolling update
kubectl rollout status deployment/agenthub-server -n agenthub

# 4. Run new migrations if needed
kubectl -n agenthub apply -f deploy/k8s/migrate-job.yaml
kubectl -n agenthub wait --for=condition=complete job/agenthub-migrate --timeout=120s
```

### Upgrading the Database Schema

```bash
# Run migrations up
agenthub migrate

# Or via kubectl
kubectl -n agenthub exec deployment/agenthub-server -- \
  agenthub-server migrate --config /etc/agenthub/config.yaml
```

### Rolling Back

```bash
# kubectl rollout undo
kubectl rollout undo deployment/agenthub-server -n agenthub

# Or use etcdctl to revert the Deployment spec
```

## Backup & Recovery

### Database Backups

Backups are handled by **Barman Cloud** with the object name `shared-pg-backup`.

- Daily incremental backups retained per policy
- Point-in-time recovery (PITR) available
- Manual snapshots via `kubectl exec`

```bash
# View backup status
kubectl get scheduledbackup -n agenthub

# Trigger manual backup (if supported by Barman)
# barman backup <cluster> --remote-state
```

### Blob Recovery

Blobs are content-addressed, so re-computation is possible if needed:

```bash
# List blobs in the store
barman list-objects <remote> <prefix>

# Restore a specific blob
# (depends on your object store implementation)
```

### Disaster Recovery

1. Provision new PostgreSQL cluster
2. Restore from latest backup
3. Re-apply Kubernetes manifests
4. Run `agenthub migrate` to re-apply schema
5. Restore blob files from backup
6. Verify: `agenthub healthz` and `agenthub catalog list`

## TLS Let's Encrypt

The ingress uses cert-manager with `letsencrypt-prod` issuer.

```yaml
# ingress.yaml snippet
apiVersion: networking.k8s.io/v1
metadata:
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
    - hosts:
        - agenthub.pilab.hu
      secretName: agenthub-tls
  rules:
    - host: agenthub.pilab.hu
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: agenthub
                port:
                  name: http
```

### Certificate Renewal

Let's Encrypt certificates renew automatically every 60-90 days.
Monitor via:

```bash
kubectl describe certificate/agenthub-tls -n agenthub
```

Check cert-manager logs:

```bash
kubectl logs -n cert-manager -l app.kubernetes.io/instance=cert-manager
```

## Scaling

### Horizontal Pod Autoscaling

```bash
kubectl autoscale deployment/agenthub-server \
  --min=2 --max=10 \
  --cpu-percent=70
```

### Resource Limits

```yaml
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: "1"
    memory: 512Mi
```

### Resource Monitoring

```bash
# Prometheus metrics endpoint
http://localhost:9100/metrics

# Key metrics:
# - go_memstats_alloc_bytes
# - go_memstats_sys_bytes
# - http_requests_total
# - http_requests_in_flight
# - go routines
```

---

*For the complete list of deployment manifests, see `deploy/` directory.*
