import json

path = "vps3-state/grafana/dashboards/benchmark-comparison-dashboard.json"
with open(path, "r") as f:
    dashboard = json.load(f)

for panel in dashboard.get("panels", []):
    if panel.get("title") == "CPU Usage: Microservice vs Serverless":
        for target in panel.get("targets", []):
            if target.get("legendFormat") == "Serverless":
                # Current expression
                expr = target.get("expr", "")
                if not " * 4" in expr:
                    # Update expression to multiply by 4
                    # Assuming it looks like: sum(rate(...)) or vector(0)
                    if " or vector(0)" in expr:
                        base = expr.replace(" or vector(0)", "")
                        target["expr"] = f"({base} * 4) or vector(0)"
                    else:
                        target["expr"] = f"({expr} * 4)"

with open(path, "w") as f:
    json.dump(dashboard, f, indent=2)
