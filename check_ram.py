import requests
url = "http://130.61.146.183:9090/api/v1/query"
query = 'sum(container_memory_working_set_bytes{job="k3s-kubelet-cadvisor", namespace="serverless", pod=~"image-process-worker-.*", container="image-process-worker"}) or vector(0)'
res = requests.get(url, params={"query": query})
print(res.text)
