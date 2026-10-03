# ML

Python side of the project. The Go platform produces candidates and evidence;
this trains and serves the models that triage, explain and verify them.

## Layout

| Directory | Purpose |
| --- | --- |
| `data/` | dataset assembly from the Go side's JSON and JSONL output |
| `train/` | fine-tuning entry points (LoRA / QLoRA) |
| `eval/` | benchmark harness, metrics, calibration |
| `serve/` | inference server, OpenAI-compatible, self-hosted |
| `configs/` | one config file per run; a notebook never holds parameters |

## Setup

```
python -m venv .venv
pip install -r ml/requirements.txt
```

## Kaggle

Notebooks live in `notebooks/kaggle/` and stay thin: pin a commit, install this
package, run one config. All real code is here so it reproduces off Kaggle.

T4 notes, learned the hard way:

- T4 is Turing: no bf16, no FlashAttention-2. Use fp16 compute with 4-bit NF4
  weights, or disable mixed precision entirely if the loss goes to `nan`.
- Keep LoRA parameters in fp32. A freshly initialised classification head
  overflows fp16 and produces `nan` validation loss.
- Gradient checkpointing is required for 8B on 16GB. Turning it off is faster
  and then runs out of memory.
- Strip every column except `input_ids`, `attention_mask` and `labels` before
  training; leftover text columns confuse the trainer.
- Sessions end at 12 hours and the quota is weekly. Checkpoint to
  `/kaggle/working/`, which is the only directory that survives.

## No commercial APIs

Nothing here calls a hosted model. Open weights, downloaded and run locally.
That constraint is the point of the project, not an inconvenience.