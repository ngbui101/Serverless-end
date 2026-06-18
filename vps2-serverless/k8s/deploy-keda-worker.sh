#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required" >&2
  exit 1
fi

sed -i 's/\r$//' ./.env
set -a
. ./.env
set +a

NODE_IP="${VPS3_PUBLIC_IP:-130.61.146.183}"
REGISTRY_URL="${REGISTRY_URL:-localhost:5000}"
POSTGRES_USER_VALUE="${POSTGRES_USER:-}"
POSTGRES_PASSWORD_VALUE="${POSTGRES_PASSWORD:-}"

if [ -z "${POSTGRES_USER_VALUE}" ] || [ -z "${POSTGRES_PASSWORD_VALUE}" ]; then
  echo "POSTGRES_USER/POSTGRES_PASSWORD are required" >&2
  exit 1
fi

kubectl apply -f k8s/namespace.yaml

kubectl -n serverless create secret generic image-process-worker-secrets \
  --from-literal=POSTGRES_HOST="${K8S_POSTGRES_HOST:-${NODE_IP}}" \
  --from-literal=POSTGRES_USER="${POSTGRES_USER_VALUE}" \
  --from-literal=POSTGRES_PASSWORD="${POSTGRES_PASSWORD_VALUE}" \
  --from-literal=MINIO_ENDPOINT="${K8S_MINIO_ENDPOINT:-http://${NODE_IP}:9000}" \
  --from-literal=MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY}" \
  --from-literal=MINIO_SECRET_KEY="${MINIO_SECRET_KEY}" \
  --from-literal=MINIO_PUBLIC_URL="${MINIO_PUBLIC_URL}" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f k8s/nats.yaml
kubectl apply -f k8s/nats-exporter.yaml
kubectl apply -f k8s/kube-state-metrics.yaml
kubectl apply -f k8s/cadvisor.yaml
kubectl apply -f k8s/prometheus-kubelet-scraper.yaml

kubectl -n serverless rollout status deploy/nats --timeout=180s
kubectl -n serverless rollout status deploy/nats-exporter --timeout=180s
kubectl -n serverless rollout status deploy/kube-state-metrics --timeout=180s

if [ -d "${HOME}/vps3-state/prometheus" ]; then
  TOKEN_FILE="${HOME}/vps3-state/prometheus/k3s-prometheus.token"
  for _ in $(seq 1 30); do
    TOKEN_B64="$(kubectl -n serverless get secret prometheus-scraper-token -o jsonpath='{.data.token}' 2>/dev/null || true)"
    if [ -n "${TOKEN_B64}" ]; then
      printf '%s' "${TOKEN_B64}" | base64 -d > "${TOKEN_FILE}"
      break
    fi
    sleep 1
  done
  if [ ! -s "${TOKEN_FILE}" ]; then
    echo "failed to write kubelet scraper token for Prometheus" >&2
    exit 1
  fi
  docker restart vps3-prometheus >/dev/null 2>&1 || true
fi

kubectl -n serverless delete pod nats-box --ignore-not-found=true
kubectl -n serverless run nats-box \
  --image=natsio/nats-box:0.14.5 \
  --restart=Never \
  --command -- sh -lc '
    set -e
    nats --server nats://nats:4222 stream add MINIO_RAW_SERVERLESS \
      --subjects minio.images.raw.serverless \
      --storage file \
      --retention limits \
      --discard old \
      --max-msgs=-1 \
      --max-bytes=-1 \
      --max-age=24h \
      --dupe-window=2m \
      --ack \
      --defaults || true
    nats --server nats://nats:4222 consumer add MINIO_RAW_SERVERLESS image-process-worker \
      --filter minio.images.raw.serverless \
      --ack explicit \
      --pull \
      --deliver all \
      --max-deliver=10 \
      --replay instant \
      --defaults || true
  '
kubectl -n serverless wait --for=jsonpath='{.status.phase}'=Succeeded pod/nats-box --timeout=120s || true
kubectl -n serverless logs nats-box || true
kubectl -n serverless delete pod nats-box --ignore-not-found=true

docker build -t "${REGISTRY_URL}/image-process-worker:latest" image-process-worker
docker push "${REGISTRY_URL}/image-process-worker:latest"

kubectl apply -f k8s/image-process-worker.yaml
kubectl -n serverless rollout status deploy/image-process-worker --timeout=180s || true

echo "KEDA worker deployment submitted."
