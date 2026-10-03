

Plan · MD
# truebug — working plan
 
Living document. Update it as things land; the design doc in `readme.md` is the
reference, this is the tracker.
 
**Thesis:** deterministic program analysis generates and verifies findings;
small fine-tuned open models triage, explain and fix them. No pretraining, no
commercial LLM APIs. The defensible assets are the dataset, the verification
system and the benchmark — not the model.
 
---
 
## Where this is right now
 
| Component | State |
| --- | --- |
| Analyzer adapters (`go vet`, `staticcheck`) → normalized JSON | Done |
| Fingerprinting stable across line shifts | Done |
| File classification (test / vendored / generated) | Done |
| Rule priors + ranking, independent of tool-reported severity | Done, priors are guesses |
| Suppression handling (`//nolint`, `//lint:ignore`) | Done |
| Hard-negative mining from maintainer suppressions | Done |
| Batch runner over a repo corpus | In progress |
| Repository indexing / snapshots | Not started |
| Knowledge graph | Not started |
| Custom Go concurrency analyzers | Not started |
| Verification ladder | Not started |
| Benchmark v0 | Not started |
| Triage classifier wired to real candidates | Not started |
 
Kaggle side: full LoRA fine-tune loop proven end to end on Qwen3-8B (4-bit,
T4 ×2). Trained on a public practice dataset, not on project data. Mechanics
work; the model itself is not useful yet.
 
---
 
## The one rule for this project
 
Static analysis first, models second. Every ML component in this plan sits
*after* a candidate generator and *before* a verifier. A model that scans all
code drowns in false positives at realistic bug rates (see design doc §2.2):
100k functions at 0.5% buggy, 80% recall and 95% specificity gives **7%
precision**. The funnel is not an optimization, it is the architecture.
 
---
 
## Phase 0 — measure before building (now)
 
The goal is a number to argue with. Nothing after this phase can be judged
without one.
 
- [ ] Batch-scan 25–30 Go repos (NVIDIA + CNCF + popular libraries)
- [ ] Record cost per repo: wall time, memory, scaling with repo size
- [ ] Build `rule_frequency.csv`: how often each rule fires, how often a human
      suppressed it
- [ ] **Replace the guessed rule priors in `internal/findings/finding.go` with
      measured suppression rates**
- [ ] Mine 500+ hard negatives from suppressions across the corpus
- [ ] Pick 15–25 repos for the benchmark; freeze the list
- [ ] Mine 100–200 bug-fix commits with linked issues and regression tests
- [ ] Hand-adjudicate a sample; measure label noise and inter-annotator
      agreement (Cohen's κ)
- [ ] Write ADRs for: analyzer set, taxonomy, fingerprint scheme, suppression
      policy
**Exit criterion:** benchmark v0 frozen, static-only baseline measured on it.
No GPU money is spent before this exists.
 
---
 
## Phase 1 — indexing and the graph
 
- [ ] Snapshot model: (repo, commit, build config), content-addressed caching
- [ ] `go/packages` + `go/ssa` loading with per-package timeouts and memory caps
- [ ] Call graph via VTA, CHA fallback, confidence recorded per edge
- [ ] Symbol monikers (SCIP-style) so findings diff across commits
- [ ] Graph tables in SQLite/Postgres; in-memory adjacency per job
- [ ] Incremental re-index: changed packages + reverse dependencies only
- [ ] **Sandbox everything** — indexing executes code (cgo, `go generate`,
      module fetches)
**Why this matters:** the Prometheus scan took ~390s for `go vet` alone on a
cold run. A PR bot at that latency is unusable. Incremental analysis is not
optional.
 
---
 
## Phase 2 — custom analyzers (the part that isn't a toolkit)
 
- [ ] Goroutine roots: spawn sites, HTTP handlers, reconcile loops
- [ ] Lockset analysis over SSA; field-based alias abstraction
- [ ] Race candidate generator: field written from ≥2 concurrency roots with no
      common lock
- [ ] Context propagation: lost cancel, context not threaded through
- [ ] Channel-leak patterns: send after receiver returned, unbuffered send with
      no reader
- [ ] **Code↔config consistency**: derive required RBAC from `client-go` /
      `controller-runtime` calls, diff against rendered Roles
- [ ] Env var / port / probe contracts between code and manifests
Check each against existing linters first (`nilaway`, `bodyclose`,
`contextcheck`, `noctx`) — do not rebuild what exists.
 
---
 
## Phase 3 — dataset
 
- [ ] Fix corpus: pre/post-fix function pairs from bug-fix commits
- [ ] Finding lifecycle across snapshots (introduced / fixed / suppressed /
      persisted)
- [ ] Hindsight labeling: teacher sees the fix and explains; student never does
- [ ] Three-way validity label (REAL_BUG / POSSIBLE / NOT_A_BUG) plus a separate
      actionability flag
- [ ] Hard negatives ≥40% of triage examples
- [ ] Time split: train < T1, val T1–T2, test > T2, with T2 after the base
      model's training cutoff
- [ ] MinHash dedup across splits; paired examples stay in the same split
**Contamination is the main threat.** If the benchmark predates the model's
cutoff, the numbers are meaningless. Report pre- and post-cutoff separately.
 
---
 
## Phase 4 — models, behind gates
 
No money spent before Gate 1. Each gate is a written decision record with
measured numbers attached.
 
| Gate | Unlocks | Condition |
| --- | --- | --- |
| G0 | nothing paid | benchmark v0 frozen, baselines measured |
| G1 | ~$65–165 | a ≤4B model beats its own zero-shot baseline; size curve still rising |
| G2 | ~$325–975 | 8B→14B gap is significant; context ablation shows gains past 4k |
| G3 | ~$650–3,250 | supervised training plateaued across two data increases |
 
- [ ] Triage classifier (0.15–1.7B) on real candidates from the corpus
- [ ] Reasoner QLoRA 1.7–8B on Kaggle
- [ ] Size sweep 0.6B → 32B to find the minimum that works
- [ ] Context ablation: does more context actually help?
---
 
## Phase 5 — verification (the differentiator)
 
Evidence ladder, in increasing strength:
 
- **L0** hypothesis — a model said so
- **L1** static fact — an analyzer proved something concrete
- **L2** multiple independent signals agree
- **L3** dynamic reproduction — test under `-race`, `goleak`, or Go 1.26's
  `goroutineleak` profile
- **L4** verified patch — fix compiles, tests pass, finding is gone
- [ ] Grounding checks: every cited `file:line` must exist in the graph
- [ ] Contradiction check against deterministic facts
- [ ] Test generation targeting a specific race hypothesis
- [ ] Sandboxed execution harness
- [ ] Calibrated confidence (isotonic), ECE target < 0.05
- [ ] Verdicts: CONFIRMED / LIKELY / POSSIBLE / REJECTED
**This is the thing nothing else does.** "This is a race, here is the test that
reproduces it" is a claim no review bot can make.
 
---
 
## Phase 6 — delivery
 
- [ ] SARIF output
- [ ] GitHub Action (lighter first integration than an App)
- [ ] GitHub App: webhook → queue → sandbox → differential analysis
- [ ] ≤2 comments per PR, report budget enforced
- [ ] `.repointel.yml` config, inline suppressions
- [ ] Private disclosure path for security findings (NVIDIA PSIRT, K8s SRC)
---
 
## Success test
 
In three months, can this sentence be true?
 
> truebug found a bug in a CNCF repository, generated a test that reproduces it
> under `-race`, and the maintainers merged the fix.
 
No review toolkit can say that. If the work is heading toward being able to say
it, the project is on the right path. If it is heading toward "we added six more
linters", it has drifted.
 
---
 
## Open questions
 
- Team size: solo, or will anyone else work on this?
- Second language after Go: Python, or C/C++ with CUDA?
- Hosting: is this ever a service, or always self-hosted only?
- Does an open tool already derive least-privilege RBAC from operator code?
  (Confirm in Phase 0 before claiming novelty.)
 

