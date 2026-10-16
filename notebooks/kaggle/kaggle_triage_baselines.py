# =============================================================================
# truebug — triage classifier baselines
#
# The previous sweep left a question it could not answer: the Qwen models
# scored AUC 0.54-0.59, barely above chance, with training losses of 3 to 12.
# Either the task is genuinely near-impossible in that framing, or our training
# was broken. Those two explanations predict the same bad numbers.
#
# This run separates them with a known reference point.
#
# CodeBERT on this exact dataset is a published baseline: the CodeXGLUE paper
# reports roughly 62% accuracy on Devign defect detection. So:
#
#   - CodeBERT lands near 62%  -> the harness is sound, and the Qwen failures
#                                 were real. The framing is the problem.
#   - CodeBERT lands near 50%  -> our training code is broken, and every number
#                                 from the last run is meaningless.
#
# Either answer is worth the GPU hours. A result you cannot interpret is not.
#
# It also settles a design decision from section 5.1: ModernBERT-large versus a
# small decoder with a classification head, for the triage role. Encoders are
# built for classification; decoders have a head bolted on. Worth measuring
# rather than assuming.
#
# HOW TO RUN
#   1. New notebook, Accelerator -> GPU T4 x2, Internet -> On
#   2. Paste this whole file into one cell
#   3. Save Version -> Save & Run All
#   4. Results in /kaggle/working/triage_baselines.{json,md}
#
# Roughly 5-6 hours. Results are written after each model.
# =============================================================================

import subprocess
import sys

subprocess.run([
    sys.executable, "-m", "pip", "install", "-q",
    "transformers", "peft", "bitsandbytes", "accelerate",
    "datasets", "scikit-learn",
], check=True)

import gc
import json
import os
import time

import numpy as np
import torch
from datasets import load_dataset
from sklearn.metrics import (
    accuracy_score,
    confusion_matrix,
    precision_recall_fscore_support,
    roc_auc_score,
)
from transformers import (
    AutoModelForSequenceClassification,
    AutoTokenizer,
    BitsAndBytesConfig,
    DataCollatorWithPadding,
    Trainer,
    TrainingArguments,
)
from peft import LoraConfig, get_peft_model, prepare_model_for_kbit_training

# -----------------------------------------------------------------------------
# configuration
# -----------------------------------------------------------------------------

# Ordered cheapest-and-most-informative first. If the session dies early, the
# reference point is already measured.
RUNS = [
    {
        # The reference. A published number exists for this exact pairing, so
        # it tells us whether to trust anything else here.
        "name": "microsoft/codebert-base",
        "kind": "encoder",
        "batch": 16,
        "accum": 1,
        "lr": 2e-5,
        "epochs": 3,
        "train_n": 0,  # 0 means the full split
    },
    {
        # The design doc's triage candidate.
        "name": "answerdotai/ModernBERT-large",
        "kind": "encoder",
        "batch": 8,
        "accum": 2,
        "lr": 2e-5,
        "epochs": 3,
        "train_n": 0,
    },
    {
        # The previous run's best model, now trained properly: ten times the
        # data and three epochs instead of one. If it was merely undertrained,
        # this is where that shows.
        "name": "Qwen/Qwen3-0.6B",
        "kind": "decoder_lora",
        "batch": 8,
        "accum": 2,
        "lr": 1e-4,
        "epochs": 2,
        "train_n": 10000,
    },
]

MAX_LEN = 512
SEED = 42

OUT_JSON = "/kaggle/working/triage_baselines.json"
OUT_MD = "/kaggle/working/triage_baselines.md"

# CodeXGLUE's reported CodeBERT accuracy on Devign defect detection.
PUBLISHED_CODEBERT_ACC = 0.6208

print("loading dataset...", flush=True)
raw = load_dataset("google/code_x_glue_cc_defect_detection")
print(raw, flush=True)


# -----------------------------------------------------------------------------
# metrics
# -----------------------------------------------------------------------------

def compute_metrics(eval_pred):
    logits, labels = eval_pred
    logits = torch.tensor(np.asarray(logits), dtype=torch.float32)
    probs = torch.softmax(logits, dim=-1)[:, 1].numpy()
    preds = (probs >= 0.5).astype(int)
    labels = np.asarray(labels).astype(int)

    precision, recall, f1, _ = precision_recall_fscore_support(
        labels, preds, average="binary", zero_division=0
    )
    tn, fp, fn, tp = confusion_matrix(labels, preds, labels=[0, 1]).ravel()

    try:
        auc = float(roc_auc_score(labels, probs))
    except ValueError:
        auc = float("nan")

    return {
        "accuracy": float(accuracy_score(labels, preds)),
        "precision": float(precision),
        "recall": float(recall),
        "f1": float(f1),
        "fpr": float(fp) / float(fp + tn) if (fp + tn) else 0.0,
        "auc": auc,
        "tp": int(tp),
        "fp": int(fp),
        "tn": int(tn),
        "fn": int(fn),
    }


def majority_baseline(split):
    """What you get by always predicting the majority class.

    Every model number below has to beat this to mean anything. On imbalanced
    data a useless classifier can look respectable on accuracy alone.
    """
    labels = np.array([int(t) for t in raw[split]["target"]])
    majority = int(labels.mean() >= 0.5)
    return {
        "accuracy": float((labels == majority).mean()),
        "predicts": majority,
        "positive_rate": float(labels.mean()),
    }


# -----------------------------------------------------------------------------
# one run
# -----------------------------------------------------------------------------

def prepare_split(split, n, tokenizer):
    ds = raw[split]
    if n and n < len(ds):
        ds = ds.shuffle(seed=SEED).select(range(n))

    ds = ds.map(
        lambda batch: tokenizer(batch["func"], truncation=True, max_length=MAX_LEN),
        batched=True,
    )
    ds = ds.map(lambda ex: {"labels": int(ex["target"])})

    keep = {"input_ids", "attention_mask", "labels"}
    return ds.remove_columns([c for c in ds.column_names if c not in keep])


def build_model(cfg, tokenizer):
    name, kind = cfg["name"], cfg["kind"]

    if kind == "encoder":
        kwargs = {"num_labels": 2}
        # ModernBERT compiles its reference implementation by default, which
        # fails on Turing. Disabling it costs speed and nothing else.
        if "ModernBERT" in name:
            kwargs["reference_compile"] = False

        model = AutoModelForSequenceClassification.from_pretrained(name, **kwargs)
        model.to("cuda")
        return model

    # Decoder with a classification head: 4-bit base, LoRA adapters.
    bnb = BitsAndBytesConfig(
        load_in_4bit=True,
        bnb_4bit_quant_type="nf4",
        bnb_4bit_compute_dtype=torch.float16,
    )
    model = AutoModelForSequenceClassification.from_pretrained(
        name, num_labels=2, quantization_config=bnb, device_map="auto"
    )
    model.config.pad_token_id = tokenizer.pad_token_id
    model = prepare_model_for_kbit_training(model, use_gradient_checkpointing=True)
    model = get_peft_model(
        model,
        LoraConfig(
            r=16,
            lora_alpha=32,
            lora_dropout=0.05,
            bias="none",
            target_modules=["q_proj", "k_proj", "v_proj", "o_proj"],
            task_type="SEQ_CLS",
        ),
    )

    # A randomly initialised head produces large logits that overflow fp16.
    for _, param in model.named_parameters():
        if param.requires_grad:
            param.data = param.data.to(torch.float32)

    model.print_trainable_parameters()
    return model


def run_one(cfg):
    name = cfg["name"]
    print("\n" + "=" * 70)
    print(f"  {name}   {cfg['kind']}  bs={cfg['batch']}x{cfg['accum']} "
          f"lr={cfg['lr']} epochs={cfg['epochs']}")
    print("=" * 70, flush=True)

    started = time.time()

    tokenizer = AutoTokenizer.from_pretrained(name)
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token

    train_ds = prepare_split("train", cfg["train_n"], tokenizer)
    val_ds = prepare_split("validation", 0, tokenizer)
    test_ds = prepare_split("test", 0, tokenizer)

    print(f"train {len(train_ds)}  val {len(val_ds)}  test {len(test_ds)}", flush=True)

    model = build_model(cfg, tokenizer)

    args = TrainingArguments(
        output_dir=f"/kaggle/working/ckpt/{name.split('/')[-1]}",
        per_device_train_batch_size=cfg["batch"],
        per_device_eval_batch_size=cfg["batch"] * 2,
        gradient_accumulation_steps=cfg["accum"],
        num_train_epochs=cfg["epochs"],
        learning_rate=cfg["lr"],
        weight_decay=0.01,
        max_grad_norm=1.0,
        eval_strategy="epoch",
        save_strategy="no",
        logging_steps=100,
        fp16=(cfg["kind"] == "encoder"),
        report_to="none",
        seed=SEED,
    )

    trainer = Trainer(
        model=model,
        args=args,
        train_dataset=train_ds,
        eval_dataset=val_ds,
        processing_class=tokenizer,
        data_collator=DataCollatorWithPadding(tokenizer=tokenizer),
        compute_metrics=compute_metrics,
    )

    train_out = trainer.train()

    # Validation is what training watched; test is the number that counts.
    val_scores = trainer.evaluate(eval_dataset=val_ds, metric_key_prefix="val")
    test_scores = trainer.evaluate(eval_dataset=test_ds, metric_key_prefix="test")

    adapter_dir = f"/kaggle/working/models/{name.split('/')[-1]}"
    model.save_pretrained(adapter_dir)
    tokenizer.save_pretrained(adapter_dir)

    result = {
        "model": name,
        "kind": cfg["kind"],
        "train_examples": len(train_ds),
        "epochs": cfg["epochs"],
        "train_loss": float(train_out.training_loss),
        "minutes": round((time.time() - started) / 60, 1),
    }
    for key, value in val_scores.items():
        result[key] = value
    for key, value in test_scores.items():
        result[key] = value

    del trainer, model
    gc.collect()
    torch.cuda.empty_cache()

    print(json.dumps(result, indent=2), flush=True)
    return result


# -----------------------------------------------------------------------------
# report
# -----------------------------------------------------------------------------

def write_report(results, baselines):
    with open(OUT_JSON, "w") as f:
        json.dump({"baselines": baselines, "results": results}, f, indent=2)

    lines = [
        "# Triage classifier baselines",
        "",
        f"CodeXGLUE defect detection, {MAX_LEN} tokens, test split "
        f"({len(raw['test'])} examples). Metrics below are on **test**.",
        "",
        "## Reference points",
        "",
        f"- Majority class on test: **{baselines['test']['accuracy']:.3f}** accuracy "
        f"(positive rate {baselines['test']['positive_rate']:.3f})",
        f"- Published CodeBERT accuracy on this dataset: **{PUBLISHED_CODEBERT_ACC:.3f}**",
        "",
        "Nothing below means anything unless it beats the majority baseline.",
        "",
        "| Model | Acc | F1 | Precision | Recall | FPR | AUC | Loss | Min |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- |",
    ]

    for r in results:
        if "error" in r:
            lines.append(f"| {r['model']} | failed: {r['error'][:60]} | | | | | | | |")
            continue
        lines.append(
            "| {model} | {test_accuracy:.3f} | {test_f1:.3f} | {test_precision:.3f} | "
            "{test_recall:.3f} | {test_fpr:.3f} | {test_auc:.3f} | "
            "{train_loss:.3f} | {minutes} |".format(**r)
        )

    codebert = next(
        (r for r in results if "codebert" in r.get("model", "").lower() and "error" not in r),
        None,
    )

    lines += ["", "## What this tells us", ""]

    if codebert is None:
        lines.append(
            "CodeBERT did not finish, so the harness is unverified and every "
            "number above is uninterpretable."
        )
    else:
        acc = codebert["test_accuracy"]
        gap = acc - PUBLISHED_CODEBERT_ACC
        lines.append(
            f"CodeBERT reached {acc:.3f} accuracy against a published "
            f"{PUBLISHED_CODEBERT_ACC:.3f}, a gap of {gap:+.3f}."
        )
        if abs(gap) < 0.04:
            lines += [
                "",
                "That reproduces the published result, so the training harness is "
                "sound. The near-chance AUC from the earlier Qwen sweep was a real "
                "finding about the task, not a bug in our code.",
                "",
                "Which means: single-function bug classification does not work well "
                "enough to be a detector, and the funnel architecture is doing real "
                "work rather than adding indirection. The model's job is to triage "
                "candidates that static analysis already flagged, where the prior is "
                "far higher than 0.5%.",
            ]
        else:
            lines += [
                "",
                "That does **not** reproduce the published result, so the harness is "
                "suspect. Fix it before drawing any conclusion from the Qwen numbers "
                "or from this table. Likely culprits: tokenization, label mapping, "
                "learning rate, or too few epochs.",
            ]

    lines += [
        "",
        "## Standing caveat",
        "",
        "This dataset is the duplicated, non-time-split kind PrimeVul showed to "
        "inflate results by an order of magnitude. Even a good number here would "
        "not transfer to real repositories. Its only use is as a harness check and "
        "a relative comparison between architectures.",
    ]

    with open(OUT_MD, "w") as f:
        f.write("\n".join(lines) + "\n")


# -----------------------------------------------------------------------------
# sweep
# -----------------------------------------------------------------------------

baselines = {"validation": majority_baseline("validation"), "test": majority_baseline("test")}
print("majority baseline:", json.dumps(baselines, indent=2), flush=True)

results = []
for cfg in RUNS:
    try:
        results.append(run_one(cfg))
    except Exception as exc:  # noqa: BLE001 - one failure must not lose the rest
        print(f"FAILED {cfg['name']}: {exc}", flush=True)
        results.append({"model": cfg["name"], "error": str(exc)})

    write_report(results, baselines)

print("\n" + "=" * 70)
print(open(OUT_MD).read())
print("saved:", OUT_JSON, OUT_MD)
print("models:", os.listdir("/kaggle/working/models") if os.path.exists("/kaggle/working/models") else "none")
