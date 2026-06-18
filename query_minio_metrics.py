import requests

url = "http://130.61.146.183:9090/api/v1/label/__name__/values"
res = requests.get(url)
metrics = res.json().get("data", [])

minio_metrics = [m for m in metrics if "minio_bucket" in m or "minio_cluster_usage" in m]
print("MinIO Usage Metrics:")
for m in minio_metrics:
    print(m)
