#!/usr/bin/env python3
"""Score csvrun.py answers against csvgen.py's answer key.

Usage: python3 -I csvscore.py <layouts.json> <csv_answers.jsonl> [--fields k1,k2,...] [--variant rows]

Prints JSON: runs, then per field and in total the mean over runs of
  correct  the answer is in the layout's key list,
  missed   the key has a header and the answer is null,
  bad      the answer is non-null and not in the key list.
Total is the sum of the per-field means. Answers go through guardPlacements rules 3-5
(internal/importer/suggest.go) first. Answers of every model in the file are pooled; run csvrun with ONLY_MODELS to score one.
"""
import argparse, collections, json, sys


def guard(answer, columns, fields):
    placed = {}
    for field, name in answer.items():
        if field not in fields:  # rule 3: unknown key
            continue
        if isinstance(name, str) and name.strip() and name in columns:  # rule 4
            placed[field] = name
    claims = collections.Counter(placed.values())
    return {f: h for f, h in placed.items() if claims[h] == 1}  # rule 5


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("layouts")
    ap.add_argument("answers")
    ap.add_argument("--fields")
    ap.add_argument("--variant")
    args = ap.parse_args()

    layouts = {l["id"]: l for l in json.load(open(args.layouts))}
    all_fields = list(next(iter(layouts.values()))["key"])
    fields = args.fields.split(",") if args.fields else all_fields
    unknown = [f for f in fields if f not in all_fields]
    if unknown:
        sys.exit(f"unknown field(s): {','.join(unknown)}")

    counts = {f: collections.Counter() for f in fields}
    runs = 0
    for line in open(args.answers):
        rec = json.loads(line)
        if rec.get("error") or not isinstance(rec.get("answer"), dict):
            continue
        if args.variant and rec["variant"] != args.variant:
            continue
        layout = layouts.get(rec["layout"])
        if layout is None:
            sys.exit(f"answer for layout {rec['layout']} is not in {args.layouts}")
        runs += 1
        placed = guard(rec["answer"], set(layout["columns"]), set(layout["key"]))
        for f in fields:
            want = [h for h in layout["key"][f] if h is not None]
            got = placed.get(f)
            if got is not None and got in want:
                counts[f]["correct"] += 1
            elif got is not None:
                counts[f]["bad"] += 1
            elif want:
                counts[f]["missed"] += 1
    if runs == 0:
        sys.exit("no scorable answers")

    mean = lambda f, k: round(counts[f][k] / runs, 4)
    out = {"runs": runs, "fields": {f: {k: mean(f, k) for k in ("correct", "missed", "bad")} for f in fields}}
    out["total"] = {k: round(sum(out["fields"][f][k] for f in fields), 4) for k in ("correct", "missed", "bad")}
    json.dump(out, sys.stdout, indent=1)
    print()


if __name__ == "__main__":
    main()
