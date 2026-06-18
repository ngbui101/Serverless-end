import requests
url = "http://130.61.146.183:9090/api/v1/targets"
res = requests.get(url)
import json
print(json.dumps(res.json(), indent=2))
