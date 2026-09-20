#!/usr/bin/env python3
"""Ask each model to map each generated spreadsheet layout; append answers to $DATA/csv_answers.jsonl (resumable).

Variants per layout: `headers` (header row only), `rows` (the first rows as CSV, as the preview returns them) and
`masked` (the same rows with letters and digits in data cells replaced). Structure layouts, whose header is not on
row 1, run only as `rows`."""
import csv, io, json, os, re, threading, time, urllib.error, urllib.request
from concurrent.futures import ThreadPoolExecutor

DATA = os.environ["DATA"]
KEY = open(os.environ["OPENROUTER_KEY_FILE"]).read().strip()
MODELS = ["google/gemini-3.5-flash-lite", "google/gemini-3.8-flash"]
FIELDS = ["invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
          "line_description", "line_quantity", "line_unit_price"]
COST_CAP = float(os.environ.get("COST_CAP", "2.0"))
OUT = os.path.join(DATA, "csv_answers.jsonl")
SAMPLE_ROWS = 5

SYSTEM = """You map the columns of a spreadsheet to the fields of a Nigerian e-invoicing import.

The importer reads one row per invoice line. Invoice-level values (number, date, buyer, currency, subtotal, VAT, total) repeat on every line of the same invoice.

Return a JSON object with exactly these keys.
For each field, the value is the exact header text of the one column that holds that field, copied character for character, or null when no column holds it. A column maps to at most one field. Leave a field null rather than map a column that holds something else.
- invoice_number: the invoice's own number. Not an order, PO, customer, account, internal record ID or payment reference.
- issue_date: the date the invoice was issued. Not a due date, delivery date or payment date.
- buyer_tin: the buyer's (customer's) Tax Identification Number. Not the seller's own TIN or an RC number.
- buyer_name: the buyer's name. Not a customer code, address, email or phone.
- currency: the currency code.
- subtotal: the invoice's amount before VAT. Not a line amount.
- vat: the invoice's VAT amount. Not a VAT rate or a line's tax.
- total: the invoice's total including VAT. Not a line amount, amount paid, balance due or amount after withholding tax.
- line_description: the line's item or service description.
- line_quantity: the line's quantity.
- line_unit_price: the line's price per unit. Not a line total.

Also return:
- header_row: the 1-based row number of the header row among the rows shown. Return 1 when only headers are shown.
- date_format: the format of the issue date values, one of "YYYY-MM-DD", "DD/MM/YYYY", "MM/DD/YYYY", "DD-MMM-YYYY", "other", or "ambiguous" when the values shown fit both DD/MM/YYYY and MM/DD/YYYY. null when there is no issue date column or no values are shown.
- decimal_separator: "." or "," as used in the amount columns, or null when no amounts are shown."""

SCHEMA = {"type": "object", "additionalProperties": False, "required": FIELDS + ["header_row", "date_format", "decimal_separator"],
          "properties": {**{f: {"type": ["string", "null"]} for f in FIELDS}, "header_row": {"type": "integer"},
                         "date_format": {"type": ["string", "null"]}, "decimal_separator": {"type": ["string", "null"]}}}


def csv_line(row):
    buf = io.StringIO()
    csv.writer(buf, lineterminator="").writerow(row)
    return buf.getvalue()


def mask(value):
    return re.sub(r"[0-9]", "9", re.sub(r"[A-Z]", "X", re.sub(r"[a-z]", "x", value)))


def user_text(layout, variant):
    if variant == "headers":
        return "The file's header row, in column order, as a JSON array:\n" + json.dumps(layout["columns"], ensure_ascii=False)
    shown = layout["rows"][: layout["header_row"] + SAMPLE_ROWS]
    intro = "The first rows of the file, as CSV. Row numbers start at 1."
    if variant == "masked":
        intro += (" In the data rows, letters are replaced by x or X and digits by 9 to hide the values;"
                  " punctuation, spacing and currency symbols are kept. The header row is not masked.")
        shown = shown[: layout["header_row"]] + [[mask(c) for c in r] for r in shown[layout["header_row"]:]]
    return intro + "\n" + "\n".join(f"Row {i}: {csv_line(r)}" for i, r in enumerate(shown, 1))


lock = threading.Lock()
spent = [0.0]


def done_keys():
    keys, total = set(), 0.0
    if os.path.exists(OUT):
        for line in open(OUT):
            r = json.loads(line)
            total += r.get("cost") or 0
            if not r.get("error"):
                keys.add((r["layout"], r["variant"], r["model"], r["run"]))
    return keys, total


def call(layout, variant, model, run):
    body = {"model": model, "temperature": 0,
            "messages": [{"role": "system", "content": SYSTEM}, {"role": "user", "content": user_text(layout, variant)}],
            "response_format": {"type": "json_schema", "json_schema": {"name": "column_mapping", "strict": True, "schema": SCHEMA}},
            "provider": {"require_parameters": True}, "usage": {"include": True}}
    rec = {"layout": layout["id"], "variant": variant, "model": model, "run": run}
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
            rec["answer"] = json.loads(resp["choices"][0]["message"]["content"])
            rec.pop("error", None)
            return rec
        except urllib.error.HTTPError as e:
            rec["error"] = f"HTTP {e.code}: {e.read().decode()[:300]}"
        except Exception as e:  # noqa: BLE001 -- recorded, retried, then reported
            rec["error"] = f"{type(e).__name__}: {str(e)[:300]}"
        time.sleep(2 * (attempt + 1))
    return rec


def main():
    layouts = json.load(open(os.path.join(DATA, "layouts.json")))
    only = os.environ.get("ONLY_LAYOUTS")
    if only:
        layouts = [l for l in layouts if l["id"] in only.split(",")]
    models = os.environ.get("ONLY_MODELS", ",".join(MODELS)).split(",")
    runs = int(os.environ.get("RUNS", "3"))
    keys, spent[0] = done_keys()
    jobs = []
    for layout in layouts:
        variants = ["rows"] if layout["category"] == "structure" else ["headers", "rows", "masked"]
        for variant in os.environ.get("ONLY_VARIANTS", ",".join(variants)).split(","):
            if variant not in variants:
                continue
            for model in models:
                for run in range(1, runs + 1):
                    if (layout["id"], variant, model, run) not in keys:
                        jobs.append((layout, variant, model, run))
    print(f"{len(jobs)} calls to make, ${spent[0]:.4f} already spent, cap ${COST_CAP}", flush=True)
    with ThreadPoolExecutor(max_workers=int(os.environ.get("WORKERS", "8"))) as pool, open(OUT, "a") as fh:
        for rec in pool.map(lambda j: call(*j), jobs):
            with lock:
                fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
                fh.flush()
            status = "ERR " + rec["error"][:120] if rec.get("error") else "ok"
            print(f'{rec["model"]:30} {rec["layout"]:24} {rec["variant"]:8} run{rec["run"]} ${rec.get("cost") or 0:.5f} {rec.get("latency_s")}s {status}', flush=True)
    print(f"spent in total ${spent[0]:.4f}", flush=True)


if __name__ == "__main__":
    main()
