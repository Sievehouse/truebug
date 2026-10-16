# =============================================================================
# truebug - corpus scan on Kaggle CPU (Phase 0)
#
# Runs the truebug batch scanner over every repository in bench/repos.txt, on
# Kaggle's machines instead of a laptop, and produces the first dataset that
# belongs to this project rather than to someone else's paper:
#
#   corpus/rule_frequency.csv  how often each rule fires, how often a human
#                              suppressed it -> replaces the guessed priors
#   corpus/negatives.jsonl     every maintainer suppression, with rationale
#   corpus/summary.csv         findings and wall time per repository
#   corpus/REPORT.md           the readable version
#   corpus.zip                 everything above, one download
#
# HOW TO RUN
#   1. New notebook. Accelerator -> None (CPU is enough; this uses no GPU
#      quota). Settings -> Turn on internet.
#   2. Paste this whole file into one cell.
#   3. Save Version -> Save & Run All (Commit).
#
# Expect 3-8 hours. Each repository has a 60-minute limit, results are merged
# after every repository, and nothing new starts after 11 hours, so a slow
# repo or the 12-hour session limit never loses the work already done.
# =============================================================================

import csv
import glob
import json
import os
import re
import shutil
import signal
import subprocess
import tarfile
import time
import urllib.request

TRUEBUG_URL = "https://github.com/Sievehouse/truebug"
REPO_LIST = "bench/repos.txt"

PER_REPO_TIMEOUT = 60 * 60
STOP_STARTING_AFTER = 11 * 60 * 60

TMP = "/tmp/tb"
OUT = "/kaggle/working/corpus"

STARTED = time.time()

os.makedirs(TMP, exist_ok=True)
os.makedirs(OUT, exist_ok=True)


def sh(cmd, cwd=None):
    print("$", cmd, flush=True)
    subprocess.run(cmd, shell=True, check=True, cwd=cwd)


# -----------------------------------------------------------------------------
# toolchain
# -----------------------------------------------------------------------------

# Latest stable Go, looked up rather than hard-coded so this keeps working.
releases = json.load(urllib.request.urlopen("https://go.dev/dl/?mode=json"))
go_version = releases[0]["version"]
tarball = os.path.join(TMP, f"{go_version}.linux-amd64.tar.gz")

if not os.path.exists(os.path.join(TMP, "go", "bin", "go")):
    print(f"downloading {go_version}...", flush=True)
    urllib.request.urlretrieve(f"https://go.dev/dl/{go_version}.linux-amd64.tar.gz", tarball)
    with tarfile.open(tarball) as tf:
        tf.extractall(TMP)

os.environ["PATH"] = f"{TMP}/go/bin:{TMP}/gopath/bin:" + os.environ["PATH"]
os.environ["GOPATH"] = f"{TMP}/gopath"
os.environ["GOCACHE"] = f"{TMP}/gocache"
# Repositories that declare a newer Go than we installed fetch their own.
os.environ["GOTOOLCHAIN"] = "auto"

sh("go version")
sh("go install honnef.co/go/tools/cmd/staticcheck@latest")
sh("staticcheck -version")

src = os.path.join(TMP, "truebug")
if not os.path.exists(src):
    sh(f"git clone --depth 1 {TRUEBUG_URL} {src}")
commit = subprocess.run(
    "git rev-parse HEAD", shell=True, cwd=src, capture_output=True, text=True
).stdout.strip()
print("truebug commit:", commit, flush=True)

batch_bin = os.path.join(TMP, "truebug-batch")
sh(f"go build -o {batch_bin} ./cmd/batch", cwd=src)


# -----------------------------------------------------------------------------
# repository list
# -----------------------------------------------------------------------------

def read_list(path):
    urls, seen = [], set()
    with open(path) as f:
        for line in f:
            line = line.split("#", 1)[0].strip()
            if line and line not in seen:
                seen.add(line)
                urls.append(line)
    return urls


def repo_name(url):
    """Same naming as the Go side: owner__name."""
    parts = url.rstrip("/").removesuffix(".git").split("/")
    clean = lambda s: re.sub(r"[^A-Za-z0-9.\-]", "_", s)
    return f"{clean(parts[-2])}__{clean(parts[-1])}" if len(parts) >= 2 else clean(url)


urls = read_list(os.path.join(src, REPO_LIST))
print(f"{len(urls)} repositories to scan", flush=True)


# -----------------------------------------------------------------------------
# merging
# -----------------------------------------------------------------------------

status = {}  # name -> {"state": ..., "seconds": ..., "detail": ...}


def merge():
    """Rebuild the corpus files from every finished per-repo directory.

    Called after each repository, so the output is always complete for what
    has run so far.
    """
    per_repo = sorted(glob.glob(os.path.join(OUT, "per_repo", "*")))

    # summary.csv
    header, rows = None, []
    for d in per_repo:
        p = os.path.join(d, "summary.csv")
        if not os.path.exists(p):
            continue
        with open(p, newline="") as f:
            r = list(csv.reader(f))
        if r:
            header = header or r[0]
            rows.extend(r[1:])
    for name, s in status.items():
        if s["state"] != "ok":
            rows.append([name, "", f"{s['state']}: {s['detail']}"] + [""] * 10 + [f"{s['seconds']:.1f}"])
    if header:
        with open(os.path.join(OUT, "summary.csv"), "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(header)
            w.writerows(sorted(rows))

    # negatives.jsonl
    with open(os.path.join(OUT, "negatives.jsonl"), "w") as out:
        for d in per_repo:
            p = os.path.join(d, "negatives.jsonl")
            if os.path.exists(p):
                with open(p) as f:
                    shutil.copyfileobj(f, out)

    # rule_frequency.csv, re-aggregated across repositories
    agg = {}
    for d in per_repo:
        p = os.path.join(d, "rule_frequency.csv")
        if not os.path.exists(p):
            continue
        with open(p, newline="") as f:
            for row in csv.DictReader(f):
                key = (row["tool"], row["rule"])
                a = agg.setdefault(key, {"category": row["category"], "reported": 0, "suppressed": 0, "repos": 0})
                a["reported"] += int(row["reported"])
                a["suppressed"] += int(row["suppressed_by_human"])
                a["repos"] += int(row["repos"])

    table = []
    for (tool, rule), a in agg.items():
        total = a["reported"] + a["suppressed"]
        table.append({
            "tool": tool,
            "rule": rule,
            "category": a["category"],
            "reported": a["reported"],
            "suppressed_by_human": a["suppressed"],
            "total": total,
            "suppression_rate": round(a["suppressed"] / total, 3) if total else 0.0,
            "repos": a["repos"],
        })
    table.sort(key=lambda r: (-r["total"], r["rule"]))

    with open(os.path.join(OUT, "rule_frequency.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(table[0].keys()) if table else ["rule"])
        w.writeheader()
        w.writerows(table)

    return table


# -----------------------------------------------------------------------------
# scanning
# -----------------------------------------------------------------------------

lists_dir = os.path.join(TMP, "lists")
work_dir = os.path.join(TMP, "work")
os.makedirs(lists_dir, exist_ok=True)
os.makedirs(work_dir, exist_ok=True)

for i, url in enumerate(urls, 1):
    name = repo_name(url)

    if time.time() - STARTED > STOP_STARTING_AFTER:
        status[name] = {"state": "skipped", "seconds": 0.0, "detail": "session time budget"}
        print(f"[{i:2d}/{len(urls)}] {name}: skipped, out of time", flush=True)
        continue

    list_file = os.path.join(lists_dir, name + ".txt")
    with open(list_file, "w") as f:
        f.write(url + "\n")

    repo_out = os.path.join(OUT, "per_repo", name)
    t0 = time.time()
    print(f"[{i:2d}/{len(urls)}] {name} ...", flush=True)

    # Own process group, so a timeout kills go vet and staticcheck too rather
    # than leaving them running in the background.
    proc = subprocess.Popen(
        [batch_bin, "-repos", list_file, "-work", work_dir, "-out", repo_out, "-jobs", "1"],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    try:
        output, _ = proc.communicate(timeout=PER_REPO_TIMEOUT)
        failed = "FAILED" in output or proc.returncode != 0
        state = "failed" if failed else "ok"
        detail = next((l for l in output.splitlines() if "FAILED" in l), "")[-120:]
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        proc.communicate()
        state, detail = "timeout", f"over {PER_REPO_TIMEOUT // 60} min"
        shutil.rmtree(repo_out, ignore_errors=True)

    secs = time.time() - t0
    status[name] = {"state": state, "seconds": secs, "detail": detail}
    print(f"      {state} in {secs / 60:.1f} min {detail}", flush=True)

    # Clones are only needed while scanning; the disk is not unlimited.
    shutil.rmtree(os.path.join(work_dir, name), ignore_errors=True)

    merge()


# -----------------------------------------------------------------------------
# report
# -----------------------------------------------------------------------------

table = merge()

negatives = sum(1 for _ in open(os.path.join(OUT, "negatives.jsonl")))
ok = [n for n, s in status.items() if s["state"] == "ok"]
bad = {n: s for n, s in status.items() if s["state"] != "ok"}

lines = [
    "# Corpus scan",
    "",
    f"- truebug commit: `{commit}`",
    f"- Go: {go_version}",
    f"- repositories scanned: **{len(ok)}** of {len(urls)}",
    f"- hard negatives mined: **{negatives}**",
    f"- wall time: {(time.time() - STARTED) / 3600:.1f} h",
    "",
]

if bad:
    lines += ["## Not scanned", "", "| Repo | State | Detail |", "| --- | --- | --- |"]
    for n, s in sorted(bad.items()):
        lines.append(f"| {n} | {s['state']} | {s['detail']} |")
    lines.append("")

lines += [
    "## Most frequent rules",
    "",
    "| Rule | Tool | Category | Reported | Suppressed | Rate | Repos |",
    "| --- | --- | --- | --- | --- | --- | --- |",
]
for r in table[:25]:
    lines.append(
        f"| {r['rule']} | {r['tool']} | {r['category']} | {r['reported']} | "
        f"{r['suppressed_by_human']} | {r['suppression_rate']:.2f} | {r['repos']} |"
    )

# A rate is only worth acting on with enough observations behind it.
dismissed = [r for r in table if r["total"] >= 10 and r["suppressed_by_human"] > 0]
dismissed.sort(key=lambda r: -r["suppression_rate"])

lines += [
    "",
    "## Rules maintainers dismiss most (10+ observations)",
    "",
    "These are the candidates for a *lower* prior in "
    "`internal/findings/finding.go`. A high rate means humans looked at this "
    "rule firing and routinely decided it was wrong.",
    "",
    "| Rule | Total | Suppressed | Rate |",
    "| --- | --- | --- | --- |",
]
for r in dismissed[:20]:
    lines.append(f"| {r['rule']} | {r['total']} | {r['suppressed_by_human']} | {r['suppression_rate']:.2f} |")

lines += [
    "",
    "## Caveats",
    "",
    "- Suppression rate is a *lower bound* on how often a rule is wrong: most "
    "false positives are never suppressed, just ignored.",
    "- A rule with zero suppressions is not proven precise; it may simply be "
    "rare or unfamiliar to maintainers.",
    "- Some suppressions hide real bugs. These are medium-noise labels.",
]

report = "\n".join(lines) + "\n"
with open(os.path.join(OUT, "REPORT.md"), "w") as f:
    f.write(report)

shutil.make_archive("/kaggle/working/corpus", "zip", OUT)

print("\n" + "=" * 70)
print(report)
print("download: /kaggle/working/corpus.zip")
