# Serverless Image Processing Benchmark

Stand: 2026-06-18

Dieses Repository enthaelt den aktuellen Benchmark-Aufbau fuer Bildverarbeitung mit zwei aktiven Pfaden:

- VPS1: klassische Go-Microservice-Baseline per HTTP MinIO Webhook.
- VPS3: Serverless-Go-Worker auf k3s mit NATS JetStream und KEDA.

Java/Spring Boot wird fuer VPS1 nicht mehr verwendet. Der Serverless-Pfad hat keinen Cron Job und keine Notification-Funktion.

## Aktuelle Architektur

| Host | Rolle | Aktiver Stand |
| --- | --- | --- |
| VPS1 | Microservice-Baseline | Go HTTP Service, Docker Compose Service `app`, Container `vps1-microservice` |
| VPS3 | Serverless und State | k3s, KEDA, NATS JetStream, MinIO, PostgreSQL, Prometheus, Grafana |
| VPS2 | Logische Rolle im Repo | Der Serverless-Code liegt unter `vps2-serverless`, laeuft produktiv aber auf VPS3 |

Echte IPs, SSH-Keys, Passwoerter und `.env` Dateien werden nicht versioniert. GitHub Actions nutzt Secrets.

## Repository-Struktur

| Pfad | Zweck |
| --- | --- |
| `vps1-microservice` | VPS1 Go Microservice fuer Classic MinIO Webhooks |
| `vps2-serverless/image-process-worker` | Go Worker fuer den Serverless-Pfad |
| `vps2-serverless/k8s` | k3s/NATS/KEDA Manifeste und Deploy-Script |
| `vps3-state` | MinIO, PostgreSQL, Prometheus und Grafana |
| `load-testing` | k6 Smoke- und Vergleichstests |

## VPS1: Go Microservice

VPS1 ist die klassische Microservice-Baseline. MinIO sendet Classic Events an:

```text
POST /webhooks/minio/classic
```

Endpoints:

```text
GET  /healthz
GET  /metrics
POST /webhooks/minio/classic
```

Verarbeitung:

1. Event JSON aus MinIO parsen.
2. Nur Bucket `images-raw-classic` verarbeiten.
3. Object Key URL-decodieren.
4. Bild aus MinIO laden.
5. PNG/JPG/JPEG dekodieren.
6. Bild auf weissem Hintergrund normalisieren.
7. WebP per `cwebp` erzeugen.
8. WebP in `images-processed` hochladen.
9. Datensatz in `processed_images` schreiben.
10. Raw-Objekt erst nach erfolgreichem Upload und DB Insert loeschen.

Wichtige Defaults:

```text
APP_PORT=8080
MINIO_BUCKET_RAW=images-raw-classic
MINIO_BUCKET_PROCESSED=images-processed
VPS1_MAX_CONCURRENT_PROCESSING=2
```

Ressourcen:

- Go Container, keine JVM.
- Runtime enthaelt `cwebp`.
- `mem_limit: 512m`.
- kleiner DB Pool.
- HTTP Timeouts aktiv.
- Semaphore begrenzt parallele Verarbeitung; bei voller Semaphore antwortet VPS1 mit `503`, damit MinIO retryen kann.

Deployment:

- Workflow: `.github/workflows/deploy-vps1.yml`
- Image: `ghcr.io/<repo>/vps1-microservice:latest`
- Compose Service: `app`
- Container: `vps1-microservice`
- Port Mapping: `${APP_PORT}:8080`
- Bestehende `.env` auf VPS1 wird nicht ueberschrieben.

Kompatible Env Vars:

```text
APP_PORT
MINIO_ENDPOINT
MINIO_ACCESS_KEY
MINIO_SECRET_KEY
MINIO_BUCKET_RAW
MINIO_BUCKET_PROCESSED
MINIO_PUBLIC_URL
SPRING_DATASOURCE_URL
SPRING_DATASOURCE_USERNAME
SPRING_DATASOURCE_PASSWORD
POSTGRES_HOST
POSTGRES_PORT
POSTGRES_DB
POSTGRES_USER
POSTGRES_PASSWORD
POSTGRES_SSLMODE
VPS1_MAX_CONCURRENT_PROCESSING
```

Wenn `SPRING_DATASOURCE_URL` gesetzt ist, wird daraus der Postgres-DSN abgeleitet. Sonst werden `POSTGRES_*` verwendet. Default fuer `sslmode` ist `disable`.

## VPS3: Serverless Worker

Serverless bleibt Go Worker + NATS JetStream + KEDA.

Flow:

1. Bild wird nach `images-raw-serverless` hochgeladen.
2. MinIO sendet ObjectCreated Events an NATS Subject `minio.images.raw.serverless`.
3. NATS Stream `MINIO_RAW_SERVERLESS` speichert die Events.
4. KEDA liest den Lag des Pull Consumers `image-process-worker`.
5. KEDA skaliert das Deployment `image-process-worker`.
6. Worker laedt das Bild aus MinIO, normalisiert es und erzeugt WebP per `cwebp`.
7. Worker schreibt WebP nach `images-processed`.
8. Worker schreibt in `processed_images`.
9. Worker loescht danach das Raw-Objekt.
10. Erst danach wird die NATS Message ACKed.

Aktuelle KEDA-Konfiguration:

```yaml
pollingInterval: 5
cooldownPeriod: 60
minReplicaCount: 0
maxReplicaCount: 10
lagThreshold: "1"
activationLagThreshold: "1"
```

Aktuelle Worker-Konfiguration:

```yaml
terminationGracePeriodSeconds: 180
NATS_BATCH_SIZE: "1"
NATS_FETCH_TIMEOUT_MS: "2000"
resources.requests.cpu: 50m
resources.requests.memory: 64Mi
resources.limits.memory: 128Mi
```

Warum Serverless an/aus geht:

- `minReplicaCount: 0` ist bewusst gesetzt.
- Wenn kein JetStream-Lag vorhanden ist, skaliert KEDA auf 0.
- Bei neuem Backlog skaliert KEDA wieder hoch.
- `cooldownPeriod: 60` haelt Instanzen nach dem letzten aktiven Lag noch etwa 60 Sekunden.
- Der Worker behandelt `SIGTERM` graceful: beim Scale-down werden keine neuen Messages mehr geholt, aber eine bereits laufende Verarbeitung wird fertig gemacht.

Wichtig: KEDA skaliert nach NATS JetStream Consumer Lag, nicht nach der Anzahl Dateien im MinIO Raw Bucket.

## State und Daten

Buckets:

```text
images-raw-classic
images-raw-serverless
images-processed
```

Postgres-Tabelle:

```sql
processed_images (
  image_id UUID PRIMARY KEY,
  original_filename TEXT NOT NULL,
  minio_processed_url TEXT NOT NULL,
  processing_time_ms BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
```

Erfolgskriterium nach Verarbeitung:

- Raw Bucket ist leer.
- `images-processed` enthaelt WebP-Dateien.
- `processed_images` enthaelt Eintraege.

## Observability

Es bleibt nur das Benchmark-Dashboard aktiv:

```text
Benchmark: Cron, Image Process, Notify
UID: benchmark-comparison
URL: /d/benchmark-comparison/benchmark3a-cron-image-process-notify
```

Andere Dashboards wurden entfernt. Loki/Alloy/minio-exporter sind fuer den aktuellen Benchmark nicht noetig. Prometheus, cAdvisor und kube-state-metrics bleiben noetig.

Aktuelle Grafana/Prometheus-Verbindung:

- Grafana Datasource UID: `Prometheus`
- Datasource URL: `http://host.docker.internal:9090`
- Grafana hat `host.docker.internal:host-gateway`.
- Prometheus scraped MinIO ueber `host.docker.internal:9000`.

Diese Host-Gateway-Route wurde gesetzt, weil die interne Docker-Bridge-Verbindung zwischen Grafana/Prometheus/MinIO auf VPS3 zeitweise hing.

Relevante Prometheus Targets:

```text
prometheus
minio
vps1-microservice
vps1-cadvisor
vps1-node-exporter
k3s-kube-state-metrics
k3s-kubelet-cadvisor
k3s-cadvisor
```

Einige alte VPS2/VPS3 node-exporter Targets koennen `down` sein und sind fuer das aktuelle Dashboard nicht entscheidend.

## Dashboard-Metriken

CPU Panel:

Microservice:

```promql
sum(rate(container_cpu_usage_seconds_total{job="vps1-cadvisor", image=~".*vps1-microservice.*", cpu="total"}[1m])) / 2
```

Grund: VPS1 hat einen physischen AMD-Core mit zwei SMT-Threads. cAdvisor zaehlt logische CPU-Zeit; `/ 2` normalisiert die Anzeige auf den physischen AMD-Core.

Serverless:

```promql
sum(rate(container_cpu_usage_seconds_total{job="k3s-kubelet-cadvisor", namespace="serverless", pod=~"image-process-worker-.*", container="image-process-worker"}[1m])) or vector(0)
```

Das ist rohe ARM-Core-Nutzung aller `image-process-worker` Pods. Es gibt keinen `* 1.5` Clock-Multiplier mehr.

Serverless Running Instances:

```promql
kube_deployment_status_replicas{job="k3s-kube-state-metrics", namespace="serverless", deployment="image-process-worker"}
kube_deployment_status_replicas_available{job="k3s-kube-state-metrics", namespace="serverless", deployment="image-process-worker"}
```

Raw Backlog:

```promql
(sum(minio_bucket_requests_total{api="putobject", bucket="images-raw-classic", job="minio"}) or vector(0))
-
(sum(minio_bucket_requests_total{api="deleteobject", bucket="images-raw-classic", job="minio"}) or vector(0))
```

Serverless:

```promql
sum(jetstream_consumer_num_pending{stream_name="MINIO_RAW_SERVERLESS", consumer_name="image-process-worker", job="k3s-nats-exporter"}) or vector(0)
```

Hinweis: Microservice ist eine Request-basierte Naeherung. Serverless nutzt den echten JetStream Consumer Pending-Wert. Fuer echte Objektpruefung MinIO direkt abfragen.

## Deployments

VPS1:

```text
.github/workflows/deploy-vps1.yml
```

VPS3 Serverless Worker:

```text
.github/workflows/deploy-vps2.yml
```

Der Workflow:

1. fuehrt `go test ./...` fuer `vps2-serverless/image-process-worker` aus,
2. kopiert `vps2-serverless/*` nach VPS3,
3. installiert/prueft k3s und KEDA,
4. legt NATS Stream/Consumer mit `nats-box` an,
5. baut und pusht `localhost:5000/image-process-worker:latest`,
6. applied `k8s/image-process-worker.yaml`.

`nats-box` wird vor dem Start geloescht, danach wird auf Phase `Succeeded` gewartet und der Pod wieder geloescht. Das verhindert Deploy-Fehler durch liegengebliebene Completed Pods.

VPS3 State und Observability:

```text
.github/workflows/deploy-vps3.yml
```

Der Workflow kopiert `vps3-state/*`, startet Docker Compose mit `--remove-orphans` und recreated Prometheus/Grafana.

## Manuelle Live-Pruefungen

VPS3 KEDA:

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl -n serverless get scaledobject image-process-worker
kubectl -n serverless get hpa keda-hpa-image-process-worker -o wide
kubectl -n serverless get deploy image-process-worker
kubectl -n serverless get pods -l app=image-process-worker -o wide
```

NATS Stream/Consumer:

```bash
kubectl -n serverless run nats-box-check \
  --image=natsio/nats-box:0.14.5 \
  --restart=Never \
  --command -- sh -lc \
  'nats --server nats://nats:4222 stream info MINIO_RAW_SERVERLESS && nats --server nats://nats:4222 consumer info MINIO_RAW_SERVERLESS image-process-worker'
```

Grafana/Prometheus:

```bash
cd ~/vps3-state
docker compose ps
curl -fsS http://127.0.0.1:9090/-/ready
curl -fsS http://127.0.0.1:9090/api/v1/targets
docker logs --since=30s vps3-grafana
```

VPS1:

```bash
docker ps --filter name=vps1-microservice
docker logs --tail=100 vps1-microservice
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/metrics
```

## Tests

Go Tests:

```powershell
cd D:\Serverless\vps1-microservice
go test ./...

cd D:\Serverless\vps2-serverless\image-process-worker
go test ./...
```

VPS1 Docker Build:

```powershell
docker build -t vps1-microservice:test vps1-microservice
```

k6 Vergleich vom PC:

```powershell
cd D:\Serverless\load-testing
powershell -ExecutionPolicy Bypass -File .\run-comparison.ps1 -VUs 10 -Duration 30s
```

VPS1 Load Tests wurden bereits erfolgreich mit 30, 60 und 100 VUs fuer je 30 Sekunden ausgefuehrt. VPS1 blieb stabil.

## Bekannte Betriebsregeln

- Nicht manuell `.env` Dateien auf den VPS ueberschreiben.
- Raw-Objekte nur nach erfolgreicher Verarbeitung loeschen lassen.
- Wenn Serverless nicht hochskaliert, zuerst JetStream Consumer Lag pruefen, nicht MinIO Bucket Count.
- Wenn Grafana leer bleibt, zuerst Datasource/Prometheus-Targets und Grafana `/api/ds/query` Logs pruefen.
- Nach Grafana-Dashboard-Deploy im Browser hart neu laden, falls eine alte Seite noch alte Panel-Requests haelt.
