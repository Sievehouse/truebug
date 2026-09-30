
# Self-Hosted Repository Intelligence Platform: Architecture and Research Plan

Sep 30, 2026 · @Bhuvansh

Verdict: feasible as a hybrid system in which deterministic program analysis generates and verifies findings, and small fine-tuned open models triage, explain and fix them. Training a foundation model from scratch is not justified. GPU compute is not the bottleneck; labels, evaluation and precision at realistic bug rates are.

## 1. Feasibility verdict

Most of the requested capabilities are feasible in v1, but through very different mechanisms. The items that depend most on model reasoning are the least reliable and should be scoped as research, not product promises.

| Capability | v1 feasibility | Primary mechanism | Role of ML |
| --- | --- | --- | --- |
| Indexing: symbols, imports, call and dependency graphs | High | SCIP indexers, `go/ssa` call graphs, tree-sitter | None |
| Known bug classes: unchecked errors, nil dereference, resource leaks, lost cancel | High | Existing analyzers (vet, staticcheck, errcheck, nilaway, bodyclose) | Triage and explanation |
| Vulnerable dependencies | High | OSV-Scanner, govulncheck (reports only reachable vulnerable functions) | Prioritization |
| Kubernetes, Helm, Docker, CI/CD misconfiguration | High | Trivy, Checkov, kube-linter, hadolint, actionlint, zizmor on rendered manifests | Context-aware triage |
| Code-to-config consistency (RBAC vs client calls, env vars, ports, probes) | High, and novel | Custom deterministic extractors over the repository graph | Explanation |
| Taint-style security bugs (injection, SSRF, path traversal) | Medium | Opengrep, CodeQL or Joern taint tracking, with model-inferred sources and sinks | Specification inference, exploitability triage |
| Concurrency: races, goroutine leaks, deadlocks | Medium statically; high where tests can confirm | Lockset candidates on SSA, then `-race` and goleak confirmation | Triage, targeted test generation |
| Cross-module wrong assumptions, suspicious changes, regressions | Low to medium (research frontier) | Contract mining plus model reasoning over retrieved context | Primary detector; expect low recall |
| Architecture problems, smells, duplication, dead code | Medium | Metrics, clone detection, Go `deadcode`, import cycles | Summaries; low severity, opt-in |
| Test coverage gaps | High | Coverage profiles joined with graph reachability | Ranks gaps by risk |
| Patch generation | Medium | Model output gated by build, test and re-analysis | Generator |

The real difficulty, in order of severity:

1. **Ground truth.** Nobody knows the full bug set of a real repository, so recall cannot be measured without deliberate benchmark design (section 17).
2. **Precision at realistic base rates.** Bugs are rare per function, so any model that scans all code drowns maintainers in false positives (math in section 2).
3. **Executing repositories.** Dynamic confirmation needs builds and tests. Go is tractable; C and C++ builds are the long pole.
4. **Label quality.** Mined bug-fix data is noisy: tangled commits, mislabeled fixes and wrong bug-introducing commits.
5. **Scale.** Kubernetes-size repositories stress whole-program analysis in memory and time, so incremental analysis is mandatory.

Not feasible, or not worth it: pretraining a competitive code model, a single end-to-end "repository in, bugs out" model, and confidence percentages without an adjudicated calibration set.

Proposed v1 targets, to be measured rather than assumed: at least 70% of posted PR comments judged valid and useful by maintainers, at most two comments per PR on average, and at least 50% precision in the top 20 findings of a full-repository scan.

## 2. Assumptions challenged

Four assumptions in the brief would push the project toward high cost and low precision, and several smaller ones need correcting. Each change below alters the architecture.

### 2.1 "Train my own model" becomes "fine-tune open weights"

Pretraining compute is roughly 6 × parameters × tokens. A 7B model on 3T tokens needs about 1.3 × 10^23 FLOPs, or roughly 87,000 H100-hours at 400 TFLOPS sustained, before ablations and failed runs. The result would still trail free Apache-2.0 code models trained on more, better-curated data.

On Kaggle the gap is absurd: even a 1.5B model on 1T tokens (9 × 10^21 FLOPs) would take decades of the weekly T4 quota.

Train from scratch only where it is cheap and nothing pretrained exists: the confidence and ranking model over engineered features, a gradient-boosted model that trains in minutes on CPU. Everything else is fine-tuning. The project's defensible assets are its data, its verification system and its benchmark.

### 2.2 "Classify code for bugs" becomes "generate candidates, then triage"

Bugs are rare per function, so a scanner is judged by specificity, not accuracy. Illustration: 100,000 functions with 0.5% buggy gives 500 bugs. A classifier with 80% recall and 95% specificity returns 400 true and 4,975 false positives, which is 7% precision.

Reaching 80% precision at the same recall needs about 99.9% specificity. Real models are far from that: on PrimeVul's deduplicated, time-split data, a 7B model fell from 68.26% F1 on BigVul to 3.09%, and GPT-4-class prompting was near random in the paired setting.

The fix is structural. Analyzers, the PR diff and similarity to known fixes produce candidates with a much higher prior probability; models only rank and verify those; every output surface has a report budget.

### 2.3 "Six specialized models" becomes "three physical models"

Six separately trained and served models multiply training runs, GPU memory and evaluation work. Instead: an embedding model (0.1–0.6B), a cross-encoder triage classifier (0.15–0.6B), and one 7–14B reasoning model.

The reasoner's tasks (verify, explain, fix, infer specifications, write tests) are LoRA adapters served together through multi-LoRA inference. Whether separate adapters beat one multi-task adapter is an experiment (RQ14), not an assumption.

### 2.4 "The model should use the knowledge graph" becomes "the graph drives retrieval and evidence"

Language models do not natively consume graphs. Graph-neural vulnerability detectors were largely evaluated on the duplicated, mislabeled datasets that later studies discredited.

The graph earns its keep in three ways: selecting context (callers, callees, field accessors, config), producing deterministic facts, and supplying ranking features. Facts reach the model as short text, for example: "field `cache` written in `reconcile()` on the goroutine spawned at line 120; read in `worker()`; no common lock held."

### 2.5 Smaller corrections

| Assumption in the brief | Replace with | Why |
| --- | --- | --- |
| One common IR for all languages | One common fact schema; deep semantics stay language-native | A shared dataflow IR costs years per language, as CodeQL and Joern show |
| Phase 1 covers Go, Python and TS/JS | Go in depth plus infra configs; Python second; TS through existing linters | NVIDIA and CNCF infrastructure code is mostly Go, and `go/types` plus `go/ssa` make cross-file analysis tractable |
| Large-repo benchmark in Phase 8 | Benchmark v0 in Phase 0 | Model size, spending and retrieval decisions all need a metric first |
| Semgrep and CodeQL are open source | Opengrep (LGPL-2.1) in the core; CodeQL as an optional plugin | Semgrep's rules license restricts SaaS and competing use; CodeQL's terms cover only OSI-licensed code and research |
| A model can verify findings | Evidence from outside the model: facts, grounding checks, execution | Self-agreement is correlated error; it is a weak feature, not verification |
| Patch simulation | Counterfactual patch check in a sandbox | Rebuild, rerun the analyzer and tests; these steps execute untrusted code |
| "Confidence: 94%" | Evidence level plus a calibrated probability | 94% means something only if 94% of such findings are real on held-out adjudicated data |
| Auto-filed GitHub issues | Private disclosure for security; opt-in, human-reviewed reports | Public vulnerability issues cause harm; unsolicited bot issues get tools banned |
| Kaggle GPUs for all training | Kaggle for encoders and QLoRA up to 8B; paid bursts after measured gates | T4 lacks BF16 and FlashAttention-2, so 7B+ runs at 8k+ tokens take a week each |

## 4. Build vs reuse

Reuse every mature parser, analyzer and serving stack. Build only what makes findings trustworthy and what nobody else has: the fact graph, cross-artifact analyzers, verification, the dataset and the benchmark.

### 4.1 Reuse

| Layer | Reuse | License notes |
| --- | --- | --- |
| Syntax | tree-sitter and its grammars | MIT |
| Precise symbols (definitions, references, implementations) | SCIP indexers: scip-go, scip-python, scip-typescript, scip-java, scip-clang | Permissive; check each indexer |
| Go semantics | `golang.org/x/tools`: go/packages, go/ssa, callgraph/vta, go/analysis | BSD-3-Clause |
| Go analyzers | vet, staticcheck, errcheck, nilaway, gosec, govulncheck; golangci-lint as a runner | Permissive, except golangci-lint (GPL-3.0: invoke the binary, never link) |
| Python and JS/TS | Ruff, Pyright, Bandit, pip-audit; ESLint, TypeScript compiler API | MIT or Apache-2.0 |
| C, C++, CUDA (later phases) | clang-tidy, Clang Static Analyzer, Infer, sanitizers, compute-sanitizer | Apache-2.0 with LLVM exception, MIT; compute-sanitizer is free but proprietary |
| Multi-language SAST | Opengrep; Joern code property graphs; CodeQL as an optional plugin | LGPL-2.1; Apache-2.0; CodeQL only for OSI-licensed code and research |
| Infrastructure | Helm, Kustomize, kubeconform, kube-linter, Trivy, Checkov, hadolint, actionlint, zizmor | Mostly Apache-2.0 or MIT; hadolint is GPL-3.0 (binary only) |
| Dependencies and secrets | OSV-Scanner with OSV data, Syft and Grype, Gitleaks | Apache-2.0, MIT; advisory data is mostly CC-BY 4.0, so keep attribution |
| Dynamic checks | Go race detector, goleak, Go native fuzzing | BSD-3, MIT |
| Model serving | vLLM or SGLang (multi-LoRA, schema-constrained JSON), TensorRT-LLM, Triton Inference Server, llama.cpp for CPU-only installs | Apache-2.0, BSD-3, MIT |
| Training | PyTorch, Transformers, PEFT, TRL, Unsloth, sentence-transformers | BSD or Apache-2.0 |
| Data processing | NeMo Curator (deduplication, filtering), DuckDB, Arrow and Parquet, DVC | Apache-2.0 or MIT |
| Storage, search, queues | PostgreSQL with pgvector, FAISS or LanceDB, NATS JetStream | Permissive |
| Sandboxing | gVisor or Firecracker; rootless containers with no network | Apache-2.0 |
| GitHub | go-github; SARIF upload to GitHub code scanning | BSD-3 |

Semgrep's own rules are under a license limited to internal, non-competing, non-SaaS use, so write your own Opengrep rules under your project license rather than vendoring theirs.

### 4.2 Build

| Component | Why it must be built |
| --- | --- |
| Fact schema and repository graph with per-commit snapshots | Existing tools keep private models; none joins code, config, tests and findings across commits |
| Go concurrency analyzers: goroutine roots, lockset over SSA, context propagation, channel-leak patterns | Existing linters are mostly intra-procedural or pattern-based |
| Kubernetes cross-artifact analyzers: RBAC from client calls, env vars, ports, probes, Helm render matrix | No open tool found in this review derives least-privilege RBAC statically from operator code; confirm in Phase 0 |
| Candidate orchestration: deduplication, clustering, per-rule precision priors | Specific to this funnel |
| Context assembler | Must be byte-identical for training examples and production prompts |
| Verification ladder and dynamic harness | The core differentiator; nothing off the shelf maps a race report back to a hypothesis |
| Confidence model and calibration | Depends on your own adjudicated data |
| Dataset miners and adjudication UI | The dataset is the main research asset |
| Benchmark harness with pooled adjudication | No benchmark covers Go, infrastructure and repository-level review together |
| GitHub App with differential PR analysis | Open-source review bots mostly wrap hosted LLM APIs |

Implementation languages: Go for the platform, because it matches the target ecosystem, ships as static binaries and calls `go/analysis` natively. Python for ML. A protobuf fact schema is the contract between them, and language-specific analyzers stay in their native language.

### 4.3 Prior art to study, not adopt wholesale

**AIxCC cyber reasoning systems.** All seven finalist systems from DARPA's 2025 challenge were open-sourced; they combine fuzzing, static analysis and LLMs, and finalists found 18 real-world vulnerabilities. They target C and Java with fuzzing-based proofs, relied on commercial LLM APIs, and a 2026 follow-up (OSS-CRS) reports they are hard to run outside the original competition infrastructure.

**IRIS (ICLR 2025).** An LLM infers taint sources and sinks, CodeQL runs whole-repository dataflow, and the LLM filters the resulting paths. On 120 validated Java vulnerabilities it found 55 against CodeQL's 27. It is the template for the security track here, with an open model and Opengrep or Joern instead of GPT-4 and CodeQL.

**LLM-assisted static analysis in the Linux kernel (LLift).** The model resolves only the cases a static analyzer cannot, which is the same division of labour proposed here.

## 5. Model strategy: which models, fine-tune or train, what size

Fine-tune open-weight models; never pretrain. Start the reasoner at 4–8B dense on Kaggle, and let a measured size sweep decide whether production needs a 9–14B dense or a \~30B mixture-of-experts (MoE) model with \~3B active parameters.

### 5.1 Candidates by role (as of September 2026)

| Role | Start with | Size | License | Trainable on Kaggle |
| --- | --- | --- | --- | --- |
| Code embedder | Qwen3-Embedding-0.6B; a \~137M code embedder (e.g. CodeRankEmbed, check license) for CPU-only installs | 0.1–0.6B | Apache-2.0 | Yes |
| Reranker | Qwen3-Reranker-0.6B | 0.6B | Apache-2.0 | Yes |
| Triage classifier | ModernBERT-large (8k context) vs Qwen3-0.6B/1.7B with a classification head | 0.15–1.7B | Apache-2.0 | Yes |
| Reasoner, research phase | Qwen3 dense ladder: 0.6B, 1.7B, 4B, 8B, 14B, 32B | 0.6–32B | Apache-2.0 | Up to 8B |
| Reasoner, production candidates | Qwen3.5-9B or 27B dense; Qwen3-Coder-30B-A3B; Nemotron 3 Nano 30B-A3B; Devstral Small 2 (24B); gpt-oss-20b | 9–30B (3–4B active for MoE) | Apache-2.0, except Nemotron (NVIDIA Open Model License) | No |
| Teacher, research only | gpt-oss-120b (fits one 80GB GPU); Qwen3.5-397B-A17B or another large open MoE | 100B+ | Apache-2.0 preferred | No |

### 5.2 Selection rules

1. **One family with a dense size ladder for experiments.** Qwen3's six dense sizes share tokenizer, recipe and license, which makes the minimum-size question (RQ6) a clean sweep. They are plain transformers, so they train on Kaggle T4s.
2. **Check kernels before planning Kaggle runs on newer architectures.** Qwen3.5 uses hybrid linear-attention layers and Nemotron 3 uses Mamba-2 layers; both depend on custom kernels that often target newer GPUs than the T4.
3. **MoE for production throughput, dense for cheap fine-tuning.** NVIDIA reports Nemotron 3 Nano (31.6B total, 3.2B active) at up to 3.3× the throughput of similar open models. But MoE fine-tuning keeps every expert in memory, so it is a cloud-GPU job.
4. **Licenses you can redistribute under.** Apache-2.0 or MIT for anything you ship or distill from. The NVIDIA Open Model License allows commercial use but is not OSI-approved, so keep Nemotron as an optional backend, not the only one.
5. **Stay model-agnostic.** All inference goes through an OpenAI-compatible HTTP API on your own vLLM, SGLang or TensorRT-LLM server (the protocol only, no external service), with adapters registered per base model. The open-weight landscape turned over several times in 2026, so re-baseline each quarter.
6. **Smaller is not automatically cheaper.** Small reasoning models can emit far more thinking tokens: Artificial Analysis measured 230–390M output tokens for Qwen3.5's 0.8–9B models on its suite, versus 98M for the 27B. Constrain output to a JSON schema and cap or disable thinking for verification calls.

### 5.3 Recommended sizes

The embedder and triage classifier stay at 0.1–0.6B because they run on every chunk and every candidate; the small end runs acceptably on CPU. The reasoner targets 8–14B dense or \~30B MoE with \~3B active, which at 4-bit fits one 24GB GPU with room for KV cache.

Anything larger serves only as a teacher or an upper-bound baseline, because self-hosting burden kills adoption. The central hypothesis to test: a fine-tuned 8B given structured evidence matches an untuned 30B+ model given naive context.

### 5.4 Fine-tuning recipe

1. **Supervised fine-tuning on hindsight labels.** A teacher that sees the fix explains the bug; the student learns to reach the verdict without the fix (section 6).
2. **Rejection sampling.** Keep only teacher outputs whose verdict matches ground truth and whose citations pass grounding checks.
3. **Preference optimization (DPO).** Pairs of correct vs incorrect verdicts and grounded vs hallucinated citations.
4. **Optional RL with verifiable rewards (GRPO).** Reward verdict correctness, citation grounding and, for fixes, build, test and re-analysis passes. Only after supervised training plateaus, and only on cloud GPUs.

Continued pretraining on Go is not needed: the base models already saw public Go code. Revisit only if error analysis shows Go-specific knowledge gaps rather than reasoning gaps.

## 6. Dataset construction

One mining pipeline feeds eight datasets. Store every example as pointers into a repository snapshot and render its text with the production context assembler, so training and serving see identical inputs.

### 6.1 Datasets

| ID | Dataset | Purpose | v0 target (Go) |
| --- | --- | --- | --- |
| D1 | Fix corpus: pre- and post-fix function pairs with commit, issue and advisory links | Positives and their paired negatives | 10–20k pairs |
| D2 | Finding lifecycle: analyzer findings tracked across snapshots (introduced, fixed, suppressed, persisted) | Triage labels at realistic prevalence | 50–100k findings |
| D3 | Hard negatives (6.4) | Teach "not a bug" | At least 40% of triage examples |
| D4 | Cross-artifact pairs: code with manifests, charts, Dockerfiles, workflows | Config-consistency checks | 1–3k |
| D5 | Retrieval pairs: finding to the context units its fix touched | Embedder and reranker training; context-recall evaluation | 10–30k |
| D6 | Verdict and explanation set, hindsight-labeled and verified | Reasoner fine-tuning | 5–10k |
| D7 | Fix set: bug plus context to a patch that passes build, tests and re-analysis | Fix adapter | 2–5k |
| D8 | Benchmark: frozen, time-split, human-adjudicated | Evaluation only | 300–500 bugs, 1–2k negatives |

### 6.2 Sources

| Source | What it gives | Caveat |
| --- | --- | --- |
| Go vulnerability database, OSV, GitHub Advisory Database | Fix commits; for Go, affected symbols, which localize bugs to functions | Security only; attribution required (CC-BY) |
| Bug-fix commits in curated repos (CNCF, Kubernetes SIGs, NVIDIA, popular Go libraries) | Natural bug and fix pairs | Tangled commits; keyword and bug-introducing-commit heuristics are noisy |
| Issues and PRs linked to fixes | Intent, impact and severity text | Label conventions differ per project |
| Our analyzer bundle run over historical snapshots | Realistic candidate distribution with outcomes | "Persisted" does not mean false positive |
| Justified suppressions (`//nolint:... // reason`, `#nosec`, `NOLINT`) | Hard negatives with rationale text | Some suppressions hide real bugs |
| GoBench: 82 real concurrency bugs and 103 bug kernels from 9 projects | Concurrency seeds and a sanity-check set | Pre-2021, so likely in pretraining data; never the headline metric |
| PrimeVul, CVEfixes, SWE-bench-style datasets, CodeReviewer | Cross-language signal; fix and review formats | Mostly C/C++, Java, Python; noisy labels; mixed licenses |
| Synthetic bug injection (drop an Unlock, an error check, a cancel) | Volume for triage pre-training | Unnatural; never used for evaluation |

### 6.3 Hindsight labeling

The key trick: the teacher labels with privileged information the student never gets. It sees the pre-fix code, the fix diff, the linked issue and the retrieved context, so its job is explanation, which is far more accurate than blind detection.

1. **Weak labels** from fix keywords, issue labels, advisory references and finding lifecycle.
2. **Teacher adjudication** by an open-weight model: verdict, bug lines, root cause, impact and severity, as schema-constrained JSON.
3. **Deterministic confirmation** where possible: the finding disappears at the fix commit, the fix touches the flagged lines, and tests added by the fix fail before and pass after.
4. **Human adjudication** of a stratified sample per source, to measure label noise. The benchmark (D8) is 100% human-adjudicated.
5. **Noise handling**: drop low-agreement examples, weight by source reliability, and filter against out-of-fold model predictions.

Each example carries a three-way validity label (REAL\_BUG, POSSIBLE, NOT\_A\_BUG) plus a separate actionability flag. A maintainer's "won't fix" is not a false positive, and the two must not be conflated.

### 6.4 Hard negatives

| Source | Example | Label noise | Use |
| --- | --- | --- | --- |
| Post-fix twin of every fixed bug | The patched function | Low to medium (fixes can be partial) | Paired training and evaluation |
| Adjudicated analyzer false positives | Lockset hit on a field owned by one goroutine after a channel handoff | Low | Highest value; training and evaluation |
| Justified suppressions | `//nolint:errcheck // read-only file` | Medium | Training; the rationale becomes explanation text |
| Version-dependent semantics | Loop-variable capture in a goroutine is safe when `go.mod` declares Go 1.22 or later | Low | Counterfactual pairs: same code, different `go.mod` |
| Documented never-failing calls | Ignored error from `bytes.Buffer.Write` or `hash.Hash.Write` | Low | Training |
| By-design privilege in node agents | `privileged: true` in a GPU driver or device-plugin DaemonSet | Low to medium | Infrastructure triage |
| Semantics-preserving rewrites of safe code | Renamed identifiers, reordered independent statements | Low | Blocks surface-pattern shortcuts |
| Long-lived untouched candidates | Finding that survived years of active edits with no reports | High | Down-weighted weak negatives only |

### 6.5 Record format

```json
{
  "id": "go/github.com/org/repo/3f9c.../pkg/controller/reconciler.go:184",
  "snapshot": {"repo": "github.com/org/repo", "commit": "3f9c...", "go_version": "1.22"},
  "candidate": {"producer": "lockset", "rule": "CONC.RACE.FIELD", "symbol": "(*Reconciler).reconcile"},
  "evidence": [{"kind": "write", "at": "reconciler.go:184"}, {"kind": "read", "at": "worker.go:88"}],
  "context_units": ["sym:(*Reconciler).reconcile", "sym:(*Worker).run", "type:Reconciler"],
  "label": {"validity": "REAL_BUG", "actionable": true, "sources": ["osv", "teacher:hindsight", "human"]},
  "root_cause": "...", "impact": "...", "severity": "high",
  "fix": {"commit": "a71e...", "diff_ref": "..."},
  "pair_id": "...", "split": "train", "fixed_at": "2025-03-02"
}
```

### 6.6 Splits and leakage

Split by time: train before T1, validate between T1 and T2, test after T2, with T2 after the base models' training-data cutoff. Also hold out whole repositories, including several NVIDIA ones, and remove near-duplicate functions across splits with MinHash, as PrimeVul did.

Paired examples stay in the same split. Test sets keep real prevalence; balance only the training set, then calibrate.

### 6.7 Licensing and release

Distribute pointers (repository, commit, path, line range) plus labels and rebuild scripts, and include code text only from permissively licensed repositories. Use Apache-2.0 or MIT teachers only, scrub secrets with Gitleaks, honor opt-out requests, and publish a datasheet.

## 7. Training strategy: Kaggle first, cloud behind gates

Kaggle carries the encoders and every reasoner up to \~8B at 2–4k tokens. Rent cloud GPUs only when a written, measured gate says a bigger model or longer context will move the benchmark.

### 7.1 Kaggle constraints that shape the plan

| Constraint (September 2026) | Consequence |
| --- | --- |
| Weekly GPU quota shown in account settings; it floats and is commonly reported near 30 hours | Budget experiments per week: about one or two serious 7–8B runs |
| Sessions end at 12 hours (GPU) or 9 hours (TPU) | Every job resumes from checkpoints; run in background commit mode |
| Two T4s (16GB each) or one P100 (16GB) | Prefer 2×T4: the P100 has no tensor cores |
| T4 is a Turing GPU | FP16 only (no BF16) and no FlashAttention-2, which needs Ampere or newer |
| About 20GB of persistent output | Push adapters and checkpoints to Hugging Face Hub or Kaggle Models at every save |
| TPU v5e-8 is also offered | Useful only with JAX or XLA stacks; skip unless porting is cheap |

### 7.2 Engineering pattern

|  |  |  |
| --- | --- | --- |
|  |  |  |
|  |  |  |

1. **Thin launchers.** All training code is a pip-installable package in the monorepo. A Kaggle notebook pins a commit SHA, installs the package and runs one config file, so every run reproduces off Kaggle.
2. **Offline preprocessing.** Tokenize and pack on CPU, then upload versioned Arrow or Parquet shards as a Kaggle Dataset. Never tokenize inside a GPU session.
3. **Resumable by design.** Checkpoint adapter, optimizer, sampler position and RNG state every N steps, with deterministic data order and auto-resume.
4. **Both GPUs.** For models up to 8B in 4-bit, run DDP with a full copy per T4 (TRL plus PEFT), or Unsloth on a single GPU.
5. **T4 numerics.** FP16 autocast with loss scaling, NF4 base weights with FP16 compute, gradient clipping, PyTorch SDPA attention and gradient checkpointing. Keep LoRA weights in FP32 if the loss spikes.
6. **Tracking and versioning.** MLflow (open source) or a free W&B tier during research. Every result is keyed by model SHA, data version, code SHA and config hash; models and datasets live on Hugging Face Hub (private until release) with DVC for data.

### 7.3 What runs where

| Stage | Job | Where | Rough GPU time per run |
| --- | --- | --- | --- |
| S0 | Zero-shot baselines of off-the-shelf models on benchmark v0 | Kaggle up to 9B at 4-bit; cloud for 14B+ | Hours |
| S1 | Embedder evaluation; contrastive fine-tune only if recall misses target | Kaggle | 2–6 h |
| S2 | Triage classifier, ModernBERT-large at 2–8k tokens | Kaggle | 4–12 h |
| S3 | Reasoner QLoRA, 1.7–8B at 2–4k tokens | Kaggle, 2×T4 | 5–30 h |
| S4 | Reasoner LoRA, 8–14B at 8–16k tokens | 1× H100 or A100 80GB | 2–10 h |
| S5 | DPO | 1× H100 | 2–8 h |
| S6 | GRPO with verifiable rewards (optional) | 4–8× H100 | 50–300 GPU-h |
| S7 | Teacher labeling with an open-weight teacher | 1–8× H100 | 10–100 GPU-h |

Planning assumption, to be replaced by measured numbers in week one: 7–8B QLoRA on one T4 processes roughly 400–600 tokens per second, while BF16 LoRA on one H100 processes roughly 5–10k. A 40M-token epoch (10k examples of 4k tokens) is then about 20–28 T4-hours versus 1–2 H100-hours.

### 7.4 Spend gates

Each gate is a short decision record with the measured numbers attached. No money is spent before Gate 1.

| Gate | Unlocks | Condition | Budget at Sept 2026 prices |
| --- | --- | --- | --- |
| G0 | Nothing paid | Benchmark v0 frozen; static-only and zero-shot baselines measured | $0 |
| G1 | First cloud burst for 8–14B LoRA | A Kaggle-trained model up to 4B beats its own zero-shot baseline significantly, the 0.6→4B size curve is still rising, and 8B iteration on Kaggle exceeds a week | 20–50 H100-hours, about $65–165 |
| G2 | Longer context and 14B+ | The 8B→14B zero-shot gap is significant, and a context ablation shows gains beyond 4k tokens | 100–300 H100-hours, about $325–975 |
| G3 | RL or 30B+ fine-tuning | Supervised training plateaued across two data increases, and error analysis blames reasoning rather than data | 200–1,000 H100-hours, about $650–3,250 |

Budgets use the September 2026 on-demand median of $3.25 per H100-hour; checkpointed jobs on spot capacity cost roughly half.

## 8. Hardware requirements and costs

The dominant compute in this project is CPU: static analysis, builds, tests and dataset mining. GPU spend through Gate 2 is hundreds of dollars, and serving costs about a cent per PR once the candidate funnel works. All figures below are planning estimates to replace with measurements.

### 8.1 Requirements by workload

| Workload | GPU | CPU and RAM | Storage |
| --- | --- | --- | --- |
| Kaggle research | 2× T4 16GB | Kaggle-provided | 20GB output plus Kaggle Datasets |
| Local development, analyzing Kubernetes-scale repos | Optional: one 24GB card runs an 8B reasoner at 4-bit | 16+ cores; 32GB for most repos, 64GB for whole-program SSA on the largest | 1–2TB NVMe |
| Dataset mining over 1–3k repos at monthly snapshots | None | Spot CPU fleet or a 32-core workstation | 1–5TB raw; 10–100GB processed |
| Self-hosted serving, small team | 1× L4 24GB or 1× L40S 48GB | 8+ cores, 32GB | \~10–40GB weights |
| Self-hosted serving, organization | 1–2× H100 80GB with FP8 and multi-LoRA | 16+ cores per worker pool | As above |
| CPU-only install | None: static tier plus small encoders on CPU; optional slow llama.cpp reasoner | 8+ cores, 32GB | Minimal |

### 8.2 Reasoner memory

| Model | Weights, 4-bit | Weights, FP8 | KV cache per 16k-token sequence (BF16) |
| --- | --- | --- | --- |
| 8B dense (Qwen3-8B) | \~5GB | \~9GB | \~2.4GB |
| 14B dense (Qwen3-14B) | \~9GB | \~15GB | \~2.7GB |
| 30B MoE, 3B active (Qwen3-30B-A3B) | \~17GB | \~31GB | \~1.6GB |

KV cache per token is 2 × layers × KV heads × head dimension × 2 bytes; Qwen3-8B's 36 layers, 8 KV heads and 128-wide heads give \~144KB per token. Hybrid models with few attention layers, such as Nemotron 3 Nano, need far less, so they fit more long contexts per GPU.

### 8.3 Cost tiers for the whole program

| Tier | Scope | GPU | CPU and storage | Human time |
| --- | --- | --- | --- | --- |
| T0 Free | Go only; encoders and QLoRA up to 8B on Kaggle; benchmark v0 over 15–25 repos | $0 | Your workstation plus Kaggle CPU sessions | 100–200 h adjudication |
| T1 Lean | Adds Gate 1–2 bursts and open-teacher labeling of 100–200k candidates | 100–350 H100-h, about $325–1,150 | About $150–350 of spot CPU | +100 h |
| T2 Serious | Adds DPO, small GRPO runs, 30B MoE LoRA, Python data | 500–1,500 H100-h, about $1.6–4.9k | About $500 | +200 h |
| T3 Lab | Large-scale RL, 30B+ full fine-tunes, many languages | 3,000–10,000+ H100-h, about $10–33k+ | $1–3k | A team |

Dollar figures use the September 2026 on-demand medians: $3.25 per H100-hour, $1.76 per A100-hour and $1.29 per L40S-hour. Spot capacity for checkpointed jobs is roughly half.

Two worked estimates behind the table. Teacher labeling of 200k candidates at \~4k input and \~500 output tokens with gpt-oss-120b on one H100 is roughly 15–25 H100-hours, about $50–80. Mining 2,000 repos at 24 snapshots, at \~20 core-minutes per analysis, is \~16,000 core-hours: a few hundred dollars of spot CPU or three weeks on a 32-core workstation.

### 8.4 Serving cost per PR and per repository

```latex
\text{cost} = N_{\text{after triage}} \times \left( \frac{T_{\text{prompt}}}{R_{\text{prefill}}} + \frac{T_{\text{output}}}{R_{\text{decode}}} \right) \times \text{price per GPU-second}
```

A typical PR sends 10–30 candidates of \~8k prompt and \~400 output tokens to the reasoner, roughly 30–90 GPU-seconds on one L4 with an 8B model: about one cent. A full scan of a Kubernetes-scale repository sends 1–3k candidates, a few GPU-hours and a few dollars. In both cases the CPU side (analysis, builds, tests) takes longer than the GPU side, which is the point of the funnel.

## 9. Repository indexing architecture

Index each commit as an immutable snapshot assembled from content-addressed per-file and per-package results. A PR then re-indexes only changed packages and their reverse dependencies.

### 9.1 Snapshot model

A snapshot is (repository, commit SHA, build configuration). The build configuration records GOOS, GOARCH, build tags and Go version, because files excluded by build tags are invisible to the type checker. Default to linux/amd64 plus the tags the repository's CI uses.

Syntactic results are cached by git blob SHA, so an unchanged file is never re-parsed on any branch. Semantic results are cached per package, keyed by its sources, its dependencies' export data and the build configuration, the same idea gopls and Bazel use.

Symbols get stable IDs from SCIP-style monikers such as `scip-go gomod github.com/org/repo v1.4.0 pkg/Type#Method().`. Stable IDs are what let findings and graph nodes be diffed across commits.

### 9.2 Pipeline stages

| Stage | What it produces | Tools | Cache unit |
| --- | --- | --- | --- |
| 1. Fetch | Partial clone (`--filter=blob:none`), sparse checkout, no LFS | git | Commit |
| 2. Inventory | Languages; generated code (`// Code generated ... DO NOT EDIT.`); vendored trees; tests; CODEOWNERS; churn and age | go-enry, git log | File blob |
| 3. Build environment | Toolchain from `go.mod`, modules through a pinned proxy, build-tag matrix; `compile_commands.json` for C/C++ later | go, sandbox | Snapshot |
| 4. Syntax | Functions, types, comments, chunk boundaries | tree-sitter | File blob |
| 5. Precise symbols | Definitions, references, implementations across packages | scip-go (others later) | Package |
| 6. Go semantics | Types, SSA, call graph (VTA, CHA fallback), goroutine spawns, lock and channel operations, field accesses, context flows | go/packages, go/ssa, callgraph/vta, custom go/analysis passes | Package |
| 7. Infrastructure | Typed Kubernetes objects; Helm charts rendered across a values matrix; Kustomize overlays; Dockerfile stages, USER, ENTRYPOINT; workflow triggers, permissions, actions | Helm, Kustomize, kubeconform schemas, BuildKit parser, actionlint parser | File or chart |
| 8. Linking | Cross-artifact edges: Dockerfile build of `./cmd/x` to main package, image, Deployment; env vars, flags, ports, probe paths, RBAC needs | Custom | Snapshot |
| 9. Embeddings | Vectors for changed chunks only | Embedder service | Chunk hash |
| 10. Persist | Graph tables, SCIP index, vectors, analyzer SARIF | PostgreSQL or SQLite, Parquet, vector index | Snapshot |

The Go team deprecated and removed the `go/pointer` analysis, so VTA is the most precise maintained call-graph algorithm in `x/tools`. Alias reasoning for lockset analysis must be built with a field-based abstraction, which is one reason concurrency findings need triage and dynamic confirmation.

### 9.3 Scale and graceful degradation

Parallelize by package with per-worker memory caps and per-stage timeouts. If VTA exceeds its budget on a giant repository, fall back to CHA plus SCIP references and mark those edges low-confidence. If the build fails, fall back to syntax-only mode and say so in the report.

Index vendored and generated code for name resolution, but exclude it from findings by default. For PRs, load the merge-base snapshot from cache, re-index changed packages and their reverse dependencies, and re-render only charts whose templates or values changed.

### 9.4 Indexing executes code

Indexing is not passive reading. Go package loading can invoke cgo and the C compiler, npm installs run lifecycle scripts, Python builds run backend hooks, and `go generate` runs arbitrary commands.

Every stage runs in the sandbox from section 14: never run `go generate` or install scripts, pass `--ignore-scripts` to npm, and allow network access only to a pinned module proxy.

## 10. Knowledge graph design

Use a typed property graph with provenance on every edge, stored in PostgreSQL (SQLite for the CLI) and loaded into an in-memory adjacency structure per job. No graph database is needed at this scale.

### 10.1 Nodes

| Node | Key properties | Produced by |
| --- | --- | --- |
| Repository, Snapshot | URL, commit, build configuration | Ingest |
| File | Path, blob SHA, language, generated, vendored, churn, age, owners | Inventory |
| Package, Module | Import path, version | go/packages |
| Symbol: function, method, type, interface, field, global, constant | Stable moniker, signature, span, exported, doc comment | SCIP plus semantics |
| CallSite | Caller, span, dispatch kind | SSA |
| ConcurrencyRoot | Goroutine spawn, HTTP handler, reconcile loop | Custom pass |
| SyncObject | Mutex, RWMutex, channel, WaitGroup, Once, atomic (field-based identity) | Custom pass |
| Test | Name, package, build tags | Inventory plus semantics |
| ConfigKey | Env var, flag, config-file key, CRD field | Extractors |
| InfraObject | Kubernetes kind and name, chart plus values profile, Dockerfile stage, workflow job | Infra indexers |
| Image, Binary | Image reference; main package it is built from | Linking |
| Dependency, Advisory | Module and version; OSV ID with affected symbols | go.mod, OSV |
| Finding, Evidence | Fingerprint, rule, severity, evidence level, verdict | Analysis and verification |

### 10.2 Edges

| Edge | From → to | Notes |
| --- | --- | --- |
| CONTAINS, DEFINES | Package → file → symbol | Structure |
| REFERENCES | Symbol → symbol | From SCIP |
| CALLS | Function → function | Dispatch kind (static, interface, closure, reflection), algorithm, confidence |
| IMPLEMENTS, EMBEDS | Type → interface or type | From go/types |
| READS, WRITES | Function → field or global | Carries the lockset held at the access |
| SPAWNS | Function → concurrency root | Records whether a context is passed |
| GUARDS | Sync object → field | Inferred when a field is consistently accessed under one lock |
| SENDS, RECEIVES, CLOSES | Function → channel | Channel lifecycle |
| TESTS, COVERS | Test → function | Static reachability; coverage when available |
| READS\_CONFIG, SETS\_CONFIG | Function or infra object → config key | Code-to-config contract |
| NEEDS\_PERMISSION, GRANTS\_PERMISSION | Binary or role → (group, resource, verb) | RBAC consistency |
| BUILDS, DEPLOYS, RUNS | Dockerfile → image → workload; workflow → script | Deployment chain |
| DEPENDS\_ON, AFFECTED\_BY | Module → dependency → advisory | With call-graph reachability |
| EVIDENCES | Evidence → finding | Audit trail |

Every edge records its producer (scip-go, VTA, CHA, tree-sitter heuristic, Helm render, model-inferred), a confidence and the snapshot. Model-inferred edges are allowed, but they can never be the only support for a high-severity finding.

### 10.3 Storage

A Kubernetes-size repository yields on the order of 10^5–10^6 nodes and 10^6–10^7 edges per snapshot, which fits in RAM. Queries are bounded walks from seed nodes, so PostgreSQL rows with snapshot IDs, an in-memory compressed adjacency per job, and Parquet exports for ML cover everything. Joern's code property graph is an optional sidecar for deep dataflow queries in C, C++ and Java.

### 10.4 Queries the graph must answer

1. Callers and callees within k hops above a confidence threshold, cut to a token budget.
2. Concurrent contexts of a function: which concurrency roots can reach it.
3. All accessors of a field, with the locks held at each access.
4. PR impact set: changed symbols to reverse calls, interface implementers, config readers and tests.
5. Required vs granted RBAC for each binary.
6. Tests that reach or cover a function.
7. Deployment chain: package to image to workload to service account and role.

Queries 2 and 3 combine into the first race-candidate generator:

```go
// Field accessed from two or more concurrency roots, at least one write, no common lock.
for _, f := range g.Fields() {
    acc := g.Accessors(f) // READS/WRITES edges, each with its lockset
    roots := map[RootID][]Access{}
    for _, a := range acc {
        for _, r := range g.RootsReaching(a.Func) {
            roots[r] = append(roots[r], a)
        }
    }
    if len(roots) < 2 || !anyWrite(acc) || len(commonLocks(acc)) > 0 {
        continue
    }
    emit(Candidate{Rule: "CONC.RACE.FIELD", Field: f, Evidence: acc})
}
```

Channel handoffs, `sync.Once`, WaitGroup joins and constructor-only writes are not filtered here. They become features for triage and a rich source of hard negatives.

### 10.5 Graph features for ranking

Fan-in, centrality, distance from entry points (main, HTTP handlers, reconcile loops), exposure to external input, churn, age, test coverage and owner count. They are cheap, deterministic and feed the confidence model in section 13.

## 11. Retrieval architecture

Context is assembled per hypothesis, mostly by deterministic graph recipes, with embeddings filling the gaps the graph cannot see. The same assembler builds training examples and production prompts, so the model never meets a context shape it was not trained on.

### 11.1 Assembly pipeline

1. **Seeds.** The candidate location plus the evidence symbols from analyzers or graph queries.
2. **Structural expansion.** A category recipe (11.2) walks the graph from the seeds.
3. **Semantic expansion.** Embedding search for sibling code in the repository, related docs, and similar historical fixes from the fix corpus as few-shot exemplars.
4. **Rerank.** A cross-encoder scores each context unit's relevance to the hypothesis.
5. **Pack.** Fill the token budget by priority. A compact facts block goes first, distant functions shrink to signatures, and every line carries its path and number so the model can cite `file:line`.
6. **Log.** Store the packed context with hashes; it is both the audit trail and a future training example.

### 11.2 Context recipes

| Category | Always include | Add when budget allows |
| --- | --- | --- |
| Concurrency | Accessing functions, spawn sites, the struct type, sync primitives, lockset facts | Constructors, relevant tests, similar past race fixes |
| Error handling | Callee signature and doc, the caller chain, the package's error-wrapping convention | Callee body, to check whether it can return an error at all |
| Resource lifecycle | Acquire and release sites on every path, defer statements | Callers that own the resource |
| Taint security | Source-to-sink path, sanitizers on the path, route registration and auth middleware | Config that enables the route |
| Kubernetes and infra | Rendered object, the values that produced it, code that reads the relevant config | Chart docs, sibling manifests |
| PR review | Diff hunks, pre- and post-change versions of changed symbols, impacted callers, touched and untouched tests | Similar past fixes in the same package |

### 11.3 Budgets

Target 8–16k tokens per verification call. The evidence block fits in a few hundred tokens, prefill cost grows with every token, and small models degrade with distractors. Measure the accuracy-versus-budget curve (RQ2) instead of assuming more context helps.

### 11.4 Agentic retrieval, later

Once recipes work, let the reasoner request up to about five lookups (definition, callers, search, file range) per finding. Small models are weaker tool users, so recipes come first and the comparison is an experiment (RQ13).

### 11.5 Retrieval metric

Measure context recall independently of the language model: the share of fix-relevant symbols (touched by the ground-truth fix, plus human-marked essentials) that land in the packed context. Report it with tokens spent, separately for single-file and cross-file bugs. A bug whose root cause never reaches the prompt cannot be found, so this metric bounds every downstream result.
