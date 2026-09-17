#!/usr/bin/env python3
"""Send each document to each model via OpenRouter; append answers to answers.jsonl (resumable)."""
import base64, json, os, subprocess, sys, threading, time, urllib.request, urllib.error
from concurrent.futures import ThreadPoolExecutor

BASE = os.environ.get("DATA", "")
KEY = ""
MODELS = ["google/gemini-3.5-flash-lite", "google/gemini-3.8-flash", "openai/gpt-5.6-luna",
          "openai/gpt-5.6-luna-pro", "qwen/qwen3.8-flash"]
FIELDS = ["invoice_number", "issue_date", "supplier_tin", "supplier_name",
          "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"]
COST_CAP = float(os.environ.get("COST_CAP", "4.0"))
OUT = os.path.join(BASE, os.environ.get("ANSWERS", "answers.jsonl"))

SYSTEM = """You extract the header fields of one invoice for a Nigerian e-invoicing system.

Return a JSON object with exactly these keys. Each value is a string, or null when the document does not print that field. Never guess, compute or infer a value that is not printed.
- invoice_number: the invoice's own number, exactly as printed. Not a purchase order, customer, account, RC or payment reference number.
- issue_date: the date the invoice was issued, as YYYY-MM-DD. Not a due date, delivery date or billing period.
- supplier_name: the seller issuing the invoice, exactly as printed.
- supplier_tin: the seller's Tax Identification Number, exactly as printed.
- buyer_name: the party being billed, exactly as printed.
- buyer_tin: the buyer's Tax Identification Number, exactly as printed.
- currency: the ISO 4217 code of the invoice currency. A printed ₦, N or Naira means NGN.
- subtotal: the amount before VAT, as digits with a decimal point and no currency symbol or thousands separators (1250000.00).
- vat: the VAT amount (not the rate), same number format.
- total: the total amount payable, same number format."""

TEXT_INTRO = """The document text below was read by a PDF parser. Each line is one visual row on the page. y is the row's vertical position and x each text run's horizontal position, both from 0 to 1 measured from the top-left corner."""
IMAGE_INTRO = "The document has no text layer. Its pages are attached as images."

SCHEMA = {"type": "object", "additionalProperties": False, "required": FIELDS,
          "properties": {f: {"type": ["string", "null"]} for f in FIELDS}}


def doc_text(dump):
    out = []
    for p in dump["pages"]:
        out.append(f"--- page {p['number']} ---")
        for ln in p["lines"] or []:
            runs = "   ".join(f'x={s["x"]:.2f} {s["text"]}' for s in ln["segments"])
            out.append(f"y={ln['y']:.3f} | {runs}")
    return "\n".join(out)


def user_content(dump):
    if dump["text_chars"] > 0:
        return [{"type": "text", "text": TEXT_INTRO + "\n\n" + doc_text(dump)}]
    parts = [{"type": "text", "text": IMAGE_INTRO}]
    for p in dump["pages"]:
        b64 = base64.b64encode(open(p["image"], "rb").read()).decode()
        parts.append({"type": "image_url", "image_url": {"url": "data:image/png;base64," + b64}})
    return parts


lock = threading.Lock()
spent = [0.0]


def done_keys():
    keys, total = set(), 0.0
    if os.path.exists(OUT):
        for line in open(OUT):
            r = json.loads(line)
            total += r.get("cost") or 0
            if not r.get("error"):
                keys.add((r["file"], r["model"], r["run"]))
    return keys, total


def call(dump, model, run):
    # An arm label "<model>+reasoning-<effort>" runs the same model with reasoning turned on.
    model_id, _, arm = model.partition("+")
    body = {"model": model_id,
            "messages": [{"role": "system", "content": SYSTEM}, {"role": "user", "content": user_content(dump)}],
            "response_format": {"type": "json_schema", "json_schema": {"name": "invoice_fields", "strict": True, "schema": SCHEMA}},
            "provider": {"require_parameters": True},
            "usage": {"include": True}}
    if not model.startswith("openai/"):  # the Luna models reject temperature
        body["temperature"] = 0
    if arm.startswith("reasoning-"):
        body["reasoning"] = {"effort": arm.removeprefix("reasoning-")}
    rec ={"file": dump["file"], "model": model, "run": run, "input": "text" if dump["text_chars"] > 0 else "image"}
    for attempt in range(3):
        with lock:
            if spent[0] >= COST_CAP:
                rec["error"] = "cost cap reached"
                return rec
        t0 = time.time()
        try:
            req = urllib.request.Request("https://openrouter.ai/api/v1/chat/completions", data=json.dumps(body).encode(),
                                         headers={"Authorization": "Bearer " + KEY, "Content-Type": "application/json"})
            resp = json.load(urllib.request.urlopen(req, timeout=240))
            rec["latency_s"] = round(time.time() - t0, 2)
            usage = resp.get("usage") or {}
            rec["cost"] = usage.get("cost") or 0
            rec["prompt_tokens"] = usage.get("prompt_tokens")
            rec["completion_tokens"] = usage.get("completion_tokens")
            rec["reasoning_tokens"] = (usage.get("completion_tokens_details") or {}).get("reasoning_tokens")
            rec["provider"] = resp.get("provider")
            with lock:
                spent[0] += rec["cost"]
            if "error" in resp:
                raise ValueError(json.dumps(resp["error"])[:300])
            content = resp["choices"][0]["message"]["content"]
            fields = json.loads(content)
            rec["fields"] = {f: fields.get(f) for f in FIELDS}
            rec.pop("error", None)
            return rec
        except urllib.error.HTTPError as e:
            rec["error"] = f"HTTP {e.code}: {e.read().decode()[:300]}"
        except Exception as e:  # noqa: BLE001 -- recorded, retried, then reported
            rec["error"] = f"{type(e).__name__}: {str(e)[:300]}"
        time.sleep(2 * (attempt + 1))
    return rec


def _nearest_existing_dir(path):
    # `git -C <missing dir>` errors out (non-zero), which the caller reads as "not inside a
    # work tree" and silently PERMITS -- failing open on a not-yet-created in-repo path.
    # Walk up to a real directory so the check actually runs against the repo.
    path = os.path.abspath(path)
    while not os.path.isdir(path):
        parent = os.path.dirname(path)
        if parent == path:
            return path
        path = parent
    return path


def main():
    global KEY
    # DATA validated before the key is read (Stage 1 validation): a refused DATA or a missing
    # key must exit before either the invoices or the key file is opened.
    if not BASE:
        sys.exit("DATA is not set")
    if subprocess.run(["git", "-C", _nearest_existing_dir(BASE), "rev-parse", "--is-inside-work-tree"],
                       capture_output=True).returncode == 0:
        if subprocess.run(["git", "-C", _nearest_existing_dir(BASE), "check-ignore", "-q", os.path.abspath(BASE)]).returncode != 0:
            sys.exit(f"DATA {BASE} is inside a git work tree and is not ignored")

    key_path = os.environ.get("OPENROUTER_KEY_FILE")
    if not key_path:
        sys.exit("OPENROUTER_KEY_FILE is not set")
    key_dir = _nearest_existing_dir(os.path.dirname(os.path.realpath(key_path)) or ".")
    if subprocess.run(["git", "-C", key_dir, "rev-parse", "--is-inside-work-tree"],
                       capture_output=True).returncode == 0:
        sys.exit(f"OPENROUTER_KEY_FILE {key_path} resolves inside a git work tree")
    KEY = open(key_path).read().strip()

    files = [l.strip() for l in open(os.path.join(BASE, "files.txt")) if l.strip()]
    only = os.environ.get("ONLY_FILES")
    if only:
        files = [f for f in files if os.path.basename(f) in only.split(",")]
    models = os.environ.get("ONLY_MODELS", ",".join(MODELS)).split(",")
    runs = int(os.environ.get("RUNS", "3"))
    keys, spent[0] = done_keys()
    jobs = []
    for path in files:
        name = os.path.basename(path)
        stem = os.path.splitext(name)[0]
        dump = json.load(open(os.path.join(BASE, "docs", stem, "dump.json")))
        if dump["text_chars"] == 0:
            continue  # Stage B already excludes these; this is a second fence (never send as images)
        for model in models:
            for run in range(1, runs + 1):
                if (name, model, run) not in keys:
                    jobs.append((dump, model, run))
    print(f"{len(jobs)} calls to make, ${spent[0]:.4f} already spent, cap ${COST_CAP}", flush=True)
    with ThreadPoolExecutor(max_workers=int(os.environ.get("WORKERS", "8"))) as pool, open(OUT, "a") as fh:
        for rec in pool.map(lambda j: call(*j), jobs):
            with lock:
                fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
                fh.flush()
            status = "ERR " + rec["error"][:120] if rec.get("error") else "ok"
            print(f'{rec["model"]:32} {rec["file"][:40]:40} run{rec["run"]} ${rec.get("cost") or 0:.5f} {rec.get("latency_s")}s {status}', flush=True)
    print(f"spent this account total ${spent[0]:.4f}", flush=True)


if __name__ == "__main__":
    main()
