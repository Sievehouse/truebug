# =============================================================================
# truebug — model size sweep (RQ6: what is the smallest model that works?)
#
# Trains the same classifier head on three model sizes with identical data and
# identical settings, and reports precision, recall, F1 and false-positive rate
# rather than loss. Loss tells you training is running; FPR tells you whether
# the thing would be usable.
#
# WHAT THIS IS NOT: a real result. The dataset (CodeXGLUE / Devign) is the kind
# of data PrimeVul showed to be inflated — duplicated functions, no time split.
# A 7B model scoring 68% F1 on data like this scored 3% once the data was
# cleaned and split by time. So read these numbers as a *relative* comparison
# between sizes and a check that the pipeline works, never as "the model is X%
# accurate at finding bugs".
#
# HOW TO RUN ON KAGGLE
#   1. New notebook, Settings -> Accelerator -> GPU T4 x2, Internet -> On
#   2. Paste this whole file into one cell
#   3. Save Version -> Save & Run All  (background job; survives closing the tab)
#   4. Results land in /kaggle/working/size_sweep.json and size_sweep.md
#
# Expect roughly 3-4 hours. Results are written after each model, so a failure
# on the largest one still leaves the smaller results on disk.
# =============================================================================

import subprocess
import sys

subprocess.run([
    sys.executable, "-m", "pip", "install", "-q",
    "transformers", "peft", "trl", "bitsandbytes", "accelerate",
    "datasets", "scikit-learn",
], check=True)

import gc
import json
import os
import time

import numpy as np
import torch
from datasets import load_dataset
from sklearn.metrics import confusion_matrix, precision_recall_fscore_support, roc_auc_score
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

# Batch size shrinks as the model grows, but gradient accumulation keeps the
# effective batch at 16 for all three. Without that the comparison would be
# confounded: a different effective batch size is a different experiment.
RUNS = [
    {"name": "Qwen/Qwen3-0.6B", "batch": 8, "accum": 2},
    {"name": "Qwen/Qwen3-1.7B", "batch": 4, "accum": 4},
    {"name": "Qwen/Qwen3-8B", "batch": 1, "accum": 16},
]

N_TRAIN = 3000
N_EVAL = 1000
MAX_LEN = 512
EPOCHS = 1
LR = 1e-4
SEED = 42

OUT_JSON = "/kaggle/working/size_sweep.json"
OUT_MD = "/kaggle/working/size_sweep.md"

print("loading dataset...")
raw = load_dataset("google/code_x_glue_cc_defect_detection")
print(raw)


# -----------------------------------------------------------------------------
# metrics
# -----------------------------------------------------------------------------

def compute_metrics(eval_pred):
    """Precision, recall, F1, FPR and AUC.

    FPR is the one that matters for this project. At realistic bug rates a
    scanner is judged by specificity: 5% FPR across 100k functions is 5,000
    false alarms, which is a tool nobody runs twice.
    """
    logits, labels = eval_pred
    logits = torch.tensor(np.asarray(logits), dtype=torch.float32)
    probs = torch.softmax(logits, dim=-1)[:, 1].numpy()
    preds = (probs >= 0.5).astype(int)
    labels = np.asarray(labels).astype(int)

    precision, recall, f1, _ = precision_recall_fscore_support(
        labels, preds, average="binary", zero_division=0
    )

    tn, fp, fn, tp = confusion_matrix(labels, preds, labels=[0, 1]).ravel()
    fpr = float(fp) / float(fp + tn) if (fp + tn) else 0.0

    try:
        auc = float(roc_auc_score(labels, probs))
    except ValueError:
        auc = float("nan")

    return {
        "precision": float(precision),
        "recall": float(recall),
        "f1": float(f1),
        "fpr": fpr,
        "auc": auc,
        "tp": int(tp),
        "fp": int(fp),
        "tn": int(tn),
        "fn": int(fn),
    }


# -----------------------------------------------------------------------------
# one run
# -----------------------------------------------------------------------------

def prepare_split(split, n, tokenizer):
    ds = raw[split].shuffle(seed=SEED).select(range(min(n, len(raw[split]))))
    ds = ds.map(
        lambda batch: tokenizer(batch["func"], truncation=True, max_length=MAX_LEN),
        batched=True,
    )
    ds = ds.map(lambda ex: {"labels": int(ex["target"])})

    # Leftover text columns confuse the trainer; keep only what the model eats.
    keep = {"input_ids", "attention_mask", "labels"}
    return ds.remove_columns([c for c in ds.column_names if c not in keep])


def run_one(cfg):
    name, batch, accum = cfg["name"], cfg["batch"], cfg["accum"]
    print("\n" + "=" * 70)
    print(f"  {name}   batch={batch} accum={accum} effective={batch * accum}")
    print("=" * 70, flush=True)

    started = time.time()

    tokenizer = AutoTokenizer.from_pretrained(name)
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token

    train_ds = prepare_split("train", N_TRAIN, tokenizer)
    eval_ds = prepare_split("validation", N_EVAL, tokenizer)

    positives = int(sum(train_ds["labels"]))
    print(f"train {len(train_ds)} ({positives} positive)  eval {len(eval_ds)}")

    # T4 is a Turing GPU: no bf16, no FlashAttention-2. 4-bit NF4 weights with
    # fp16 compute is the only configuration that fits an 8B model in 16GB.
    bnb = BitsAndBytesConfig(
        load_in_4bit=True,
        bnb_4bit_quant_type="nf4",
        bnb_4bit_compute_dtype=torch.float16,
    )

    model = AutoModelForSequenceClassification.from_pretrained(
        name, num_labels=2, quantization_config=bnb, device_map="auto"
    )
    model.config.pad_token_id = tokenizer.pad_token_id

    # Gradient checkpointing is required, not optional: without it an 8B model
    # runs out of memory partway through the first epoch.
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

    # The classification head starts randomly initialised and produces large
    # logits. In fp16 those overflow and the loss becomes nan on the first
    # step, so every trainable parameter stays in fp32.
    for _, param in model.named_parameters():
        if param.requires_grad:
            param.data = param.data.to(torch.float32)

    model.print_trainable_parameters()

    args = TrainingArguments(
        output_dir=f"/kaggle/working/ckpt/{name.split('/')[-1]}",
        per_device_train_batch_size=batch,
        per_device_eval_batch_size=batch,
        gradient_accumulation_steps=accum,
        num_train_epochs=EPOCHS,
        learning_rate=LR,
        max_grad_norm=0.3,
        eval_strategy="epoch",
        save_strategy="no",
        logging_steps=25,
        fp16=False,  # fp16 autocast on top of 4-bit weights goes unstable on T4
        report_to="none",
        seed=SEED,
    )

    trainer = Trainer(
        model=model,
        args=args,
        train_dataset=train_ds,
        eval_dataset=eval_ds,
        processing_class=tokenizer,
        data_collator=DataCollatorWithPadding(tokenizer=tokenizer),
        compute_metrics=compute_metrics,
    )

    train_out = trainer.train()
    scores = trainer.evaluate()

    adapter_dir = f"/kaggle/working/adapters/{name.split('/')[-1]}"
    model.save_pretrained(adapter_dir)
    tokenizer.save_pretrained(adapter_dir)

    result = {
        "model": name,
        "params_trainable": sum(p.numel() for p in model.parameters() if p.requires_grad),
        "train_examples": len(train_ds),
        "eval_examples": len(eval_ds),
        "train_loss": float(train_out.training_loss),
        "minutes": round((time.time() - started) / 60, 1),
        "adapter": adapter_dir,
    }
    for key, value in scores.items():
        result[key.replace("eval_", "")] = value

    # Free the GPU before the next, larger model loads.
    del trainer, model
    gc.collect()
    torch.cuda.empty_cache()

    print(json.dumps(result, indent=2), flush=True)
    return result


# -----------------------------------------------------------------------------
# sweep
# -----------------------------------------------------------------------------

def write_report(results):
    with open(OUT_JSON, "w") as f:
        json.dump(results, f, indent=2)

    lines = [
        "# Size sweep (RQ6)",
        "",
        f"Dataset: CodeXGLUE defect detection, {N_TRAIN} train / {N_EVAL} eval, "
        f"{MAX_LEN} tokens, {EPOCHS} epoch, LoRA r=16, 4-bit NF4, 2x T4.",
        "",
        "| Model | F1 | Precision | Recall | FPR | AUC | Loss | Minutes |",
        "| --- | --- | --- | --- | --- | --- | --- | --- |",
    ]

    for r in results:
        if "error" in r:
            lines.append(f"| {r['model']} | failed: {r['error']} | | | | | | |")
            continue
        lines.append(
            "| {model} | {f1:.3f} | {precision:.3f} | {recall:.3f} | "
            "{fpr:.3f} | {auc:.3f} | {train_loss:.3f} | {minutes} |".format(**r)
        )

    lines += [
        "",
        "## Reading this",
        "",
        "These numbers do not say how well the models find bugs. The dataset is",
        "the duplicated, non-time-split kind that PrimeVul showed to inflate",
        "results by an order of magnitude. Treat it as a relative comparison",
        "between sizes and a check that the training pipeline is sound.",
        "",
        "The question it does answer: does the 8B model justify its cost over",
        "the 0.6B on the same task? If the curve is flat, the small-model thesis",
        "survives its first test and the project can stay on free GPUs longer.",
        "",
        "AUC near 0.5 means the model is guessing, whatever the F1 says: on",
        "imbalanced data a classifier that always predicts the majority class",
        "can look respectable on accuracy and be worth nothing.",
    ]

    with open(OUT_MD, "w") as f:
        f.write("\n".join(lines) + "\n")


results = []
for cfg in RUNS:
    try:
        results.append(run_one(cfg))
    except Exception as exc:  # noqa: BLE001 - one model failing must not lose the rest
        print(f"FAILED {cfg['name']}: {exc}", flush=True)
        results.append({"model": cfg["name"], "error": str(exc)})

    # Written after every model, so a crash on the 8B keeps the smaller results.
    write_report(results)

print("\n" + "=" * 70)
print(open(OUT_MD).read())
print("saved:", OUT_JSON, OUT_MD)
print("adapters:", os.listdir("/kaggle/working/adapters") if os.path.exists("/kaggle/working/adapters") else "none")
