# 2026-10-04: size sweep and triage baselines

Two Kaggle runs (2x T4) on CodeXGLUE / Devign defect detection: classify one
C function, in isolation, as buggy or not.

## Run 1: size sweep (RQ6)

3,000 train / 1,000 eval, 512 tokens, 1 epoch, LoRA r=16, 4-bit NF4.

| Model | F1 | FPR | AUC | Minutes |
| --- | --- | --- | --- | --- |
| Qwen3-0.6B | 0.483 | 0.293 | 0.583 | 20.7 |
| Qwen3-1.7B | 0.467 | 0.271 | 0.590 | 31.8 |
| Qwen3-8B | 0.449 | 0.322 | 0.538 | 91.0 |

All three near chance. Bigger was slower and slightly worse. Training losses of
3 to 12 (chance is ~0.69) mean the runs were also undertrained, so this alone
could not say whether the task or the harness was at fault.

## Run 2: triage baselines, with a published reference point

Full train split. Metrics on the **test** split.

| Model | Params | Acc | AUC | FPR | Minutes |
| --- | --- | --- | --- | --- | --- |
| Majority class | - | 0.541 | 0.500 | - | 0 |
| CodeBERT | 125M | 0.660 | 0.732 | 0.188 | 67 |
| Qwen3-0.6B, LoRA, 10k ex, 2 ep | 600M | 0.625 | 0.661 | 0.248 | 137 |
| ModernBERT-large | 395M | failed: bad `reference_compile` kwarg (our bug) | | | |

CodeBERT's published accuracy on this dataset is 0.621. We got 0.660, so the
harness reproduces the literature and Run 1's near-chance numbers were a real
property of the task, not a bug in our code.

## What it settles

1. **Encoder over decoder for triage.** A 125M encoder beat a 600M decoder with
   a classification head, in half the time. The triage classifier should be an
   encoder.
2. **More training helped Qwen (AUC 0.58 -> 0.66) but not enough.** Undertraining
   was part of Run 1's story, not all of it.
3. **Single-function classification cannot be a detector.** Best model,
   100k functions at 0.5% buggy: 500 x 0.48 = 240 caught, 99,500 x 0.188 =
   18,700 false alarms, **precision 1.3%**, about 78 false positives per real
   bug. On a dataset known to be inflated.

That is the funnel argument from design doc section 2.2, now measured on our
own hardware: models triage candidates that static analysis already flagged,
where the prior is far above 0.5%. They never scan raw code.

## Not settled

- ModernBERT-large, because of our bug. Low priority now.
- Anything about Go: this data is C, duplicated and not time-split.

## Next

Stop training on other people's data. Run `notebooks/kaggle/kaggle_corpus_scan.py`
(CPU, no GPU quota) to build our own: rule frequencies and mined hard negatives
across the NVIDIA and CNCF corpus.
