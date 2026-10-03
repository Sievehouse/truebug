# Benchmark

## Corpus

- `repos.txt` — the full corpus: NVIDIA Go projects, CNCF projects, and widely
  used Go libraries.
- `repos-small.txt` — a short list for quick runs on a laptop.

## Running

```
go run ./cmd/batch -repos bench/repos.txt -jobs 1
```

Large repositories (cilium, containerd, minio) are slow and memory-hungry. On a
laptop use `-jobs 1` and the small list.

## Outputs

| File | Contents |
| --- | --- |
| `results/repos/<owner>__<name>.json` | full report per repository |
| `results/summary.csv` | volume and wall time per repository |
| `results/rule_frequency.csv` | per-rule fire count, human suppression count, suppression rate |
| `results/negatives.jsonl` | mined hard negatives, merged |

## What to look at first

`rule_frequency.csv`. The `suppression_rate` column is how often a human
dismissed a rule that fired. A rule suppressed 60% of the time has not earned a
prior of 85 in `internal/findings/finding.go`.

Only human suppressions count toward that rate. Vendored and generated code is
excluded, because nobody judged it.

## What this is not, yet

This is a corpus, not a benchmark. A benchmark needs **known bugs** with
pre-fix and post-fix pairs, a time split, and human adjudication. That is
Phase 0 in `plan.md` and it is the thing that makes every later number mean
something.