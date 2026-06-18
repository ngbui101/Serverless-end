import json

file_path = "vps3-state/grafana/dashboards/benchmark-comparison-dashboard.json"
with open(file_path, "r", encoding="utf-8") as f:
    data = json.load(f)

# Update titles and legends
for panel in data.get("panels", []):
    if panel.get("id") == 1:
        panel["title"] = "CPU-Auslastung: Microservice vs. Serverless"
    elif panel.get("id") == 2:
        panel["title"] = "RAM-Auslastung: Microservice vs. Serverless"
    elif panel.get("id") == 6:
        panel["title"] = "Serverless-laufende-Instances"
        for t in panel.get("targets", []):
            if t.get("refId") == "A":
                t["legendFormat"] = "Serverless (skalierte Instanzen)"
            elif t.get("refId") == "B":
                t["legendFormat"] = "Serverless (bereite Instanzen)"
    elif panel.get("id") == 7:
        panel["title"] = "Vergleich Rohdaten in Backlog"
        for t in panel.get("targets", []):
            if t.get("refId") == "A":
                t["legendFormat"] = "Microservice Raw Backlog"
            elif t.get("refId") == "B":
                t["legendFormat"] = "Serverless Raw Backlog"
        # Update color overrides
        for override in panel.get("fieldConfig", {}).get("overrides", []):
            matcher = override.get("matcher", {})
            if matcher.get("options") == "Microservice raw backlog":
                matcher["options"] = "Microservice Raw Backlog"
            elif matcher.get("options") == "Serverless raw backlog":
                matcher["options"] = "Serverless Raw Backlog"

data["title"] = "Benchmark: Bildverarbeitung"
data["version"] = 13

with open(file_path, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=2)
