import json

path = "vps3-state/grafana/dashboards/benchmark-comparison-dashboard.json"
with open(path, "r") as f:
    dashboard = json.load(f)

for panel in dashboard.get("panels", []):
    if panel.get("title", "").startswith("RAM"):
        for target in panel.get("targets", []):
            if target.get("legendFormat") == "Serverless RAM":
                # Replace the expression with deployment replicas
                target["expr"] = '(sum(container_memory_working_set_bytes{job="k3s-kubelet-cadvisor", namespace="serverless", pod=~"image-process-worker-.*", container="image-process-worker"}) or vector(0)) * clamp_max(sum(kube_deployment_status_replicas{namespace="serverless", deployment="image-process-worker"}) or vector(0), 1)'

with open(path, "w") as f:
    json.dump(dashboard, f, indent=2)
