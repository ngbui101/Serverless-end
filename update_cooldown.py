import os

filepath = r"d:\Serverless\vps2-serverless\k8s\image-process-worker.yaml"
with open(filepath, "r") as f:
    content = f.read()

content = content.replace("cooldownPeriod: 60", "cooldownPeriod: 15")

with open(filepath, "w") as f:
    f.write(content)
