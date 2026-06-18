#!/usr/bin/env sh
set -eu

until mc alias set benchmark "${MINIO_ENDPOINT:-http://minio:9000}" "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" >/dev/null 2>&1; do
  sleep 2
done

RAW_BUCKET="${MINIO_BUCKET_RAW_SERVERLESS:-images-raw-serverless}"

mc admin config set benchmark notify_nats:keda \
  address="${MINIO_NOTIFY_NATS_ADDRESS:-host.docker.internal:4222}" \
  subject="${MINIO_NOTIFY_NATS_SUBJECT:-minio.images.raw.serverless}" \
  queue_dir="${MINIO_NOTIFY_NATS_QUEUE_DIR:-/tmp/minio-nats-events}" \
  queue_limit="${MINIO_NOTIFY_NATS_QUEUE_LIMIT:-100000}" \
  comment="serverless-keda-worker"

if [ "${MINIO_SKIP_RESTART:-0}" != "1" ]; then
  mc admin service restart --json benchmark || true
  until mc alias set benchmark "${MINIO_ENDPOINT:-http://minio:9000}" "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" >/dev/null 2>&1; do
    sleep 2
  done
fi

mc event remove "benchmark/${RAW_BUCKET}" arn:minio:sqs::vps2:webhook --event put || true
mc event add "benchmark/${RAW_BUCKET}" arn:minio:sqs::keda:nats --event put
mc event list "benchmark/${RAW_BUCKET}"
