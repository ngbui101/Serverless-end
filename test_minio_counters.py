import requests
url = "http://130.61.146.183:9090/api/v1/query"
query = 'minio_bucket_requests_total{api="putobject"}'
res = requests.get(url, params={"query": query})
print(res.text)
