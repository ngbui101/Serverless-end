#!/bin/sh
set -eu

until mc alias set benchmark "${MINIO_ENDPOINT}" "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" >/dev/null 2>&1; do
  sleep 2
done

mc mb --ignore-existing "benchmark/${MINIO_BUCKET_RAW_CLASSIC}"
mc mb --ignore-existing "benchmark/${MINIO_BUCKET_RAW_SERVERLESS}"
mc mb --ignore-existing "benchmark/${MINIO_BUCKET_PROCESSED}"

mc anonymous set none "benchmark/${MINIO_BUCKET_RAW_CLASSIC}"
mc anonymous set none "benchmark/${MINIO_BUCKET_RAW_SERVERLESS}"
mc anonymous set none "benchmark/${MINIO_BUCKET_PROCESSED}"

mc admin config set benchmark notify_nats:classic \
  address="${VPS1_NATS_ADDRESS:-92.5.54.80:4222}" \
  subject="MINIO_RAW_CLASSIC" \
  queue_dir="/tmp/minio-nats-events-classic" \
  queue_limit="100000" \
  comment="classic-nats-worker"

mc event add "benchmark/${MINIO_BUCKET_RAW_CLASSIC}" arn:minio:sqs::classic:nats --event put || true

mc admin config set benchmark notify_nats:keda \
  address="${MINIO_NOTIFY_NATS_ADDRESS:-host.docker.internal:4222}" \
  subject="${MINIO_NOTIFY_NATS_SUBJECT:-minio.images.raw.serverless}" \
  queue_dir="${MINIO_NOTIFY_NATS_QUEUE_DIR:-/tmp/minio-nats-events}" \
  queue_limit="${MINIO_NOTIFY_NATS_QUEUE_LIMIT:-100000}" \
  comment="serverless-keda-worker"

mc event remove "benchmark/${MINIO_BUCKET_RAW_SERVERLESS}" arn:minio:sqs::vps2:webhook --event put || true
mc event add "benchmark/${MINIO_BUCKET_RAW_SERVERLESS}" arn:minio:sqs::keda:nats --event put || true
