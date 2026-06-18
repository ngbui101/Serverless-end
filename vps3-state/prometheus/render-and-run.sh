#!/bin/sh
set -eu

cat >/etc/prometheus/prometheus.yml <<EOF
global:
  scrape_interval: 1s
  evaluation_interval: 1s

scrape_configs:
  - job_name: "prometheus"
    static_configs:
      - targets: ["127.0.0.1:${PROMETHEUS_PORT:-9090}"]

  - job_name: "minio"
    metrics_path: /minio/v2/metrics/bucket
    scrape_interval: 1s
    scrape_timeout: 1s
    static_configs:
      - targets: ["host.docker.internal:9000"]

  - job_name: "vps1-node-exporter"
    static_configs:
      - targets: ["${VPS1_NODE_EXPORTER_TARGET}"]

  - job_name: "vps2-node-exporter"
    static_configs:
      - targets: ["${VPS2_NODE_EXPORTER_TARGET}"]

  - job_name: "vps3-node-exporter"
    static_configs:
      - targets: ["${VPS3_NODE_EXPORTER_TARGET}"]

  - job_name: "vps1-microservice"
    metrics_path: /metrics
    static_configs:
      - targets: ["${VPS1_METRICS_TARGET}"]

  - job_name: "vps1-cadvisor"
    static_configs:
      - targets: ["${VPS1_CADVISOR_TARGET}"]

  - job_name: "vps2-cadvisor"
    static_configs:
      - targets: ["${VPS2_CADVISOR_TARGET}"]

  - job_name: "k3s-kube-state-metrics"
    static_configs:
      - targets: ["${K3S_KUBE_STATE_METRICS_TARGET:-host.docker.internal:30180}"]

  - job_name: "k3s-cadvisor"
    static_configs:
      - targets: ["${K3S_CADVISOR_TARGET:-host.docker.internal:30181}"]

  - job_name: "k3s-kubelet-cadvisor"
    scheme: https
    metrics_path: /metrics/cadvisor
    bearer_token_file: /etc/prometheus/k3s-prometheus.token
    tls_config:
      insecure_skip_verify: true
    static_configs:
      - targets: ["${K3S_KUBELET_TARGET:-host.docker.internal:10250}"]

  - job_name: "k3s-nats-exporter"
    scrape_interval: 1s
    scrape_timeout: 1s
    static_configs:
      - targets: ["${K3S_NATS_EXPORTER_TARGET:-host.docker.internal:7778}"]
EOF

exec /bin/prometheus \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.path=/prometheus \
  --web.enable-remote-write-receiver \
  --web.enable-lifecycle
