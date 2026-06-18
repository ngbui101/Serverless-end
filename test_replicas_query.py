import requests
url = "http://130.61.146.183:9090/api/v1/query"
query = 'sum(kube_deployment_status_replicas{namespace="serverless", deployment="image-process-worker"})'
res = requests.get(url, params={"query": query})
print(res.text)
