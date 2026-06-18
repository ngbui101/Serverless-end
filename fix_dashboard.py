import json

file_path = "vps3-state/grafana/dashboards/benchmark-comparison-dashboard.json"
with open(file_path, "r") as f:
    dashboard = json.load(f)

for panel in dashboard.get("panels", []):
    for target in panel.get("targets", []):
        if "interval" in target and target["interval"] == "1s":
            del target["interval"]
            print(f"Removed interval from panel {panel.get('title')} target {target.get('refId')}")

with open(file_path, "w") as f:
    json.dump(dashboard, f, indent=2)
print("Done")
