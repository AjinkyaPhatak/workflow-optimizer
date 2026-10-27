"""Executed only in a disposable container with no network or host mounts."""
import base64
import contextlib
import io
import json
import os
import pathlib
import sys
import pandas as pd

request = json.loads(sys.stdin.read(20 * 1024 * 1024))
work = pathlib.Path("/tmp/work")
work.mkdir()
os.chdir(work)
data = request.get("rows", [])
df = pd.DataFrame(data)
df.to_csv("input.csv", index=False)
stream = io.StringIO()
with contextlib.redirect_stdout(stream):
    if request.get("code"):
        exec(compile(request["code"], "<analysis>", "exec"),
             {"df": df, "pd": pd, "__name__": "__main__"})
    else:
        numeric = df.apply(lambda col: pd.to_numeric(col.astype(str).str.replace(",", ""),
                                                    errors="coerce"))
        numeric = numeric.dropna(axis=1, how="all")
        if numeric.empty:
            print("No numeric columns were found.")
        else:
            summary = numeric.describe().round(6)
            summary.to_csv("statistics.csv")
            columns = [str(column).replace("|", "\\|") for column in summary.columns]
            print("| Statistic | " + " | ".join(columns) + " |")
            print("| --- | " + " | ".join("---:" for _ in columns) + " |")
            for label, values in summary.iterrows():
                print("| " + str(label) + " | " + " | ".join(f"{value:.6g}" for value in values) + " |")
artifacts = []
for path in sorted(work.iterdir()):
    if path.name == "input.csv" or not path.is_file() or path.is_symlink():
        continue
    if path.suffix.lower() not in {".csv", ".json", ".txt", ".md", ".png", ".svg"}:
        continue
    if path.stat().st_size > 5 * 1024 * 1024 or len(artifacts) >= 8:
        continue
    artifacts.append({"name": path.name, "data": base64.b64encode(path.read_bytes()).decode()})
print(json.dumps({"text": stream.getvalue()[:100000], "artifacts": artifacts}, allow_nan=False))
