import { describe, expect, it } from "vitest";

import { sparkPath, trendPath } from "./chartPath";
import { EMPTY_BUCKET, type HistoryRow, type RollupBucket } from "./dashboard";
import {
  drawnDays,
  historyView,
  scopedDays,
  watToday,
  type DayTotals,
} from "./dashboardHistory";

const TODAY = "2026-10-29";

function addDays(day: string, n: number): string {
  return new Date(Date.parse(day + "T00:00:00Z") + n * 86400000)
    .toISOString()
    .slice(0, 10);
}

function row(day: string, o: Partial<HistoryRow> = {}): HistoryRow {
  return {
    day,
    entity_id: "E1",
    total: 10,
    readiness_num: 5,
    needs_attention: 1,
    not_submitted: 2,
    vat_kobo: 100,
    ...o,
  };
}

// n consecutive days ending on `end`; `f` shapes each row by index.
function run(
  end: string,
  n: number,
  f: (i: number) => Partial<HistoryRow> = () => ({}),
): HistoryRow[] {
  return Array.from({ length: n }, (_, i) =>
    row(addDays(end, i - (n - 1)), f(i)),
  );
}

// Bucket whose live point equals {total, readiness_num, needs_attention, not_submitted, vat_kobo}.
function bucket(
  o: {
    total?: number;
    num?: number;
    na?: number;
    ns?: number;
    vat?: number;
  } = {},
): RollupBucket {
  const { total = 10, num = 5, na = 1, ns = 2, vat = 100 } = o;
  return {
    counts: {
      draft: ns,
      validated: 0,
      queued: 0,
      submitted: 0,
      accepted: total - ns,
      rejected: 0,
      failed: 0,
    },
    needs_attention: na,
    awaiting_approval: 0,
    metrics: {
      readiness: { num, den: total },
      vat_tracked: { num: vat, den: 0 },
    },
    top_violations: [],
  };
}

const view = (
  rows: unknown,
  b: RollupBucket = bucket(),
  today = TODAY,
  inhouse = false,
  entity: string | null = "E1",
) => historyView(rows, inhouse, entity, b, today);

function noNaN(v: ReturnType<typeof historyView>) {
  expect(JSON.stringify(v)).not.toContain("NaN");
}

describe("scopedDays", () => {
  it("scopedDays firm keeps only the active entity's rows", () => {
    const rows = [
      row("2026-10-27", { total: 1 }),
      row("2026-10-27", { entity_id: "E2", total: 50 }),
      row("2026-10-28", { total: 2 }),
      row("2026-10-28", { entity_id: "E2", total: 60 }),
      row("2026-10-29", { total: 3 }),
      row("2026-10-29", { entity_id: "E2", total: 70 }),
    ];
    expect(scopedDays(false, "E1", rows).map((d) => d.total)).toEqual([
      1, 2, 3,
    ]);
  });

  it("scopedDays firm with a null entity returns nothing", () => {
    expect(scopedDays(false, null, [row("2026-10-27")])).toEqual([]);
  });

  it("scopedDays in-house sums raw fields per day, oldest first", () => {
    const rows = [
      row("2026-10-28", {
        entity_id: "B",
        total: 4,
        readiness_num: 1,
        needs_attention: 1,
        not_submitted: 1,
        vat_kobo: 10,
      }),
      row("2026-10-27", {
        entity_id: "A",
        total: 1,
        readiness_num: 1,
        needs_attention: 0,
        not_submitted: 0,
        vat_kobo: 1,
      }),
      row("2026-10-28", {
        entity_id: "A",
        total: 2,
        readiness_num: 2,
        needs_attention: 2,
        not_submitted: 2,
        vat_kobo: 20,
      }),
      row("2026-10-27", {
        entity_id: "B",
        total: 3,
        readiness_num: 0,
        needs_attention: 1,
        not_submitted: 1,
        vat_kobo: 2,
      }),
    ];
    expect(scopedDays(true, null, rows)).toEqual([
      {
        day: "2026-10-27",
        total: 4,
        readiness_num: 1,
        needs_attention: 1,
        not_submitted: 1,
        vat_kobo: 3,
      },
      {
        day: "2026-10-28",
        total: 6,
        readiness_num: 3,
        needs_attention: 3,
        not_submitted: 3,
        vat_kobo: 30,
      },
    ]);
  });

  it("scopedDays treats a non-array as no rows", () => {
    for (const bad of [undefined, null, {}]) {
      expect(scopedDays(false, "E1", bad)).toEqual([]);
      expect(scopedDays(true, null, bad)).toEqual([]);
    }
  });
});

describe("drawnDays", () => {
  const live = (day: string, total: number): DayTotals => ({
    day,
    total,
    readiness_num: 0,
    needs_attention: 0,
    not_submitted: 0,
    vat_kobo: 0,
  });
  const rec = (n: number, end: string) => scopedDays(false, "E1", run(end, n));

  it("drawnDays replaces a row dated today with the live value", () => {
    const recorded = scopedDays(
      false,
      "E1",
      run(TODAY, 3, (i) => (i === 2 ? { total: 5 } : {})),
    );
    const out = drawnDays(recorded, live(TODAY, 7));
    expect(out).toHaveLength(3);
    expect(out[2].total).toBe(7);
  });

  it("drawnDays appends the live value when today has no row", () => {
    const out = drawnDays(rec(3, addDays(TODAY, -1)), live(TODAY, 7));
    expect(out).toHaveLength(4);
    expect(out[3]).toEqual(live(TODAY, 7));
  });

  it("drawnDays keeps the last 84 entries", () => {
    const out = drawnDays(rec(100, addDays(TODAY, -1)), live(TODAY, 7));
    expect(out).toHaveLength(84);
    expect(out[83].total).toBe(7);
    expect(out[0].day).toBe(addDays(TODAY, -83));
  });
});

describe("historyView threshold", () => {
  it("historyView returns null views with no recorded day", () => {
    expect(view([])).toEqual({ sparks: null, trend: null });
  });

  it("historyView returns null views with one recorded day, today", () => {
    expect(view([row(TODAY)])).toEqual({ sparks: null, trend: null });
  });

  it("historyView returns null views with one recorded day, yesterday (live does not count)", () => {
    expect(view([row(addDays(TODAY, -1))])).toEqual({
      sparks: null,
      trend: null,
    });
  });

  it("historyView draws from the second recorded day", () => {
    const v = view(run(TODAY, 2));
    expect(v.sparks).toHaveLength(4);
    expect(v.trend).not.toBeNull();
  });
});

describe("historyView sparks", () => {
  it("sparks over two points are the corner-to-corner path", () => {
    const rows = [row("2026-10-28", { total: 1 }), row(TODAY, { total: 3 })];
    expect(view(rows, bucket({ total: 3 })).sparks?.[0]).toBe(
      "M3.0 27.0 L85.0 3.0",
    );
  });

  it("sparks over equal values are a flat path, not NaN", () => {
    const v = view(
      run(TODAY, 3, () => ({ total: 5 })),
      bucket({ total: 5, num: 2, ns: 1 }),
    );
    expect(v.sparks?.[0]).toBe("M3.0 27.0 L44.0 27.0 L85.0 27.0");
    noNaN(v);
  });

  it("sparks over 40 days draw the last 30 only", () => {
    const v = view(
      run(TODAY, 40, (i) => ({ total: i + 1 })),
      bucket({ total: 40 }),
    );
    expect(v.sparks?.[0]).toBe(
      sparkPath(Array.from({ length: 30 }, (_, i) => i + 11)),
    );
  });

  it("sparks use all 30 days at exactly 30, the last 30 at 31", () => {
    const f = (i: number) => ({ total: i + 1 });
    expect(view(run(TODAY, 30, f), bucket({ total: 30 })).sparks?.[0]).toBe(
      sparkPath(Array.from({ length: 30 }, (_, i) => i + 1)),
    );
    expect(view(run(TODAY, 31, f), bucket({ total: 31 })).sparks?.[0]).toBe(
      sparkPath(Array.from({ length: 30 }, (_, i) => i + 2)),
    );
  });

  it("sparks map the four fields in tile order", () => {
    const f = (i: number) => ({
      total: 10,
      vat_kobo: i * i,
      needs_attention: 10 - i,
      not_submitted: i % 2 ? 5 : 0,
    });
    const v = view(
      run(TODAY, 5, f),
      bucket({ total: 10, vat: 16, na: 6, ns: 0 }),
    );
    expect(v.sparks?.[1]).toBe(sparkPath([0, 1, 4, 9, 16]));
    expect(v.sparks?.[2]).toBe(sparkPath([10, 9, 8, 7, 6]));
    expect(v.sparks?.[3]).toBe(sparkPath([0, 5, 0, 5, 0]));
  });
});

describe("historyView trend", () => {
  // Two recorded days then a live bucket; readiness ratios first/last in percent of 100.
  const pair = (first: number, last: number) =>
    view(
      [
        row("2026-10-21", { total: 100, readiness_num: first }),
        row("2026-10-28", { total: 100, readiness_num: last }),
      ],
      bucket({ total: 100, num: last }),
      "2026-10-28",
    );

  it("trend point is the rounded ratio", () => {
    const v = view(
      run(TODAY, 2, () => ({ total: 3, readiness_num: 2 })),
      bucket({ total: 3, num: 2, ns: 0 }),
    );
    expect(v.trend?.line).toBe(trendPath([67, 67]).line);
    expect(v.trend?.now).toBe(67);
  });

  it("trend in-house sums before it divides", () => {
    const rows = [
      ...run(TODAY, 2, () => ({
        entity_id: "A",
        total: 10,
        readiness_num: 10,
      })),
      ...run(TODAY, 2, () => ({ entity_id: "B", total: 90, readiness_num: 0 })),
    ];
    const v = view(rows, bucket({ total: 100, num: 10 }), TODAY, true, null);
    expect(v.trend?.now).toBe(10);
    expect(v.trend?.line).toBe(trendPath([10, 10]).line);
  });

  it("trend delta label rises, falls and holds", () => {
    expect(pair(60, 67).trend?.deltaLabel).toBe("▲ +7 pts since 21 Oct");
    expect(pair(70, 67).trend?.deltaLabel).toBe("▼ -3 pts since 21 Oct");
    expect(pair(67, 67).trend?.deltaLabel).toBe("no change since 21 Oct");
  });

  it("trend now is the live value", () => {
    const rows = run(addDays(TODAY, -1), 3, () => ({
      total: 100,
      readiness_num: 60,
    }));
    expect(view(rows, bucket({ total: 100, num: 67 })).trend?.now).toBe(67);
  });

  it("trend skips a day with total 0", () => {
    const rows = run(addDays(TODAY, -1), 3, (i) =>
      i === 1
        ? { total: 0, readiness_num: 0 }
        : { total: 10, readiness_num: 5 },
    );
    const v = view(rows);
    expect(v.trend?.line).toBe(trendPath([50, 50, 50]).line);
    noNaN(v);
  });

  it("trend is null when fewer than two points have a ratio", () => {
    const rows = [
      row("2026-10-27", { total: 0, readiness_num: 0 }),
      row("2026-10-28", { total: 10 }),
    ];
    const v = view(rows, bucket({ total: 0, num: 0, ns: 0 }));
    expect(v.trend).toBeNull();
    noNaN(v);
  });

  it("historyView survives a bucket with no metrics", () => {
    const v = view(
      run(addDays(TODAY, -1), 2, (i) => ({ vat_kobo: 100 - i * 50 })),
      EMPTY_BUCKET,
    );
    expect(v.sparks).toHaveLength(4);
    // live vat_kobo reads 0, the last point of the vat series (100, 50, 0)
    expect(v.sparks?.[1]).toBe(sparkPath([100, 50, 0]));
    noNaN(v); // live total 0 has no ratio, so the curve ends on the last recorded point
  });

  it("trend months come from the real dates", () => {
    const v = view(run("2026-12-02", 43), bucket(), "2026-12-02");
    expect(v.trend?.months.map((m) => m.label)).toEqual(["OCT", "NOV", "DEC"]);
    expect(
      view(run("2026-10-29", 5), bucket(), "2026-10-29").trend?.months.map(
        (m) => m.label,
      ),
    ).toEqual(["OCT"]);
  });

  it("trend month offsets follow the first drawn day", () => {
    const m = view(run("2026-11-30", 41), bucket(), "2026-11-30").trend?.months;
    expect(m?.[0]).toEqual({ label: "OCT", at: 0 });
    expect(m?.[1].label).toBe("NOV");
    expect(m?.[1].at).toBeCloseTo(11 / 40, 10);
  });

  it("trend month offsets follow the index, not the calendar, across a gap", () => {
    const rows = ["2026-10-30", "2026-11-02"].map((d) => row(d));
    const m = view(rows, bucket(), "2026-11-03").trend?.months;
    expect(m).toEqual([
      { label: "OCT", at: 0 },
      { label: "NOV", at: 0.5 },
    ]);
  });

  it("trend drops a month label that would overlap the previous one", () => {
    const m = view(run("2026-12-28", 60), bucket(), "2026-12-28").trend?.months;
    // 60 days ending 12-28 start 10-30: NOV starts at index 2 (2/59 < 0.1), DEC at index 32
    expect(m?.map((x) => x.label)).toEqual(["OCT", "DEC"]);
  });

  it("trend meta names the real span", () => {
    expect(view(run("2026-10-29", 9), bucket(), "2026-10-29").trend?.meta).toBe(
      "SINCE 21 OCT · 9 DAYS",
    );
  });

  it("trend meta counts across a month end", () => {
    const rows = [row("2026-10-30"), row("2026-10-31"), row("2026-11-01")];
    expect(view(rows, bucket(), "2026-11-02").trend?.meta).toBe(
      "SINCE 30 OCT · 4 DAYS",
    );
  });

  it("trend meta reads 12 WEEKS from an 84-day span, not before", () => {
    expect(view(run(TODAY, 84), bucket()).trend?.meta).toBe("12 WEEKS");
    expect(view(run(TODAY, 83), bucket()).trend?.meta).toBe(
      `SINCE ${"8 AUG"} · 83 DAYS`,
    );
  });
});

describe("historyView adversarial", () => {
  it("firm view ignores every other client rows, including their huge totals", () => {
    const rows = [
      ...run(TODAY, 3, (i) => ({ total: i + 1 })),
      ...run(TODAY, 3, () => ({
        entity_id: "E2",
        total: 9999,
        readiness_num: 1,
      })),
    ];
    const v = view(rows, bucket({ total: 3 }));
    expect(v.sparks?.[0]).toBe(sparkPath([1, 2, 3]));
    expect(v.trend?.now).toBe(Math.round((5 / 3) * 100));
  });

  it("a stale row dated today is replaced by the live value in the sparks", () => {
    const rows = [
      row("2026-10-27", { total: 1 }),
      row("2026-10-28", { total: 9 }),
      row(TODAY, { total: 5 }),
    ];
    expect(view(rows, bucket({ total: 9 })).sparks?.[0]).toBe(
      sparkPath([1, 9, 9]),
    );
  });

  it("a stale row dated today is replaced by the live value in the trend", () => {
    const rows = [
      row("2026-10-27", { total: 100, readiness_num: 50 }),
      row(TODAY, { total: 100, readiness_num: 10 }),
    ];
    expect(view(rows, bucket({ total: 100, num: 80 })).trend?.now).toBe(80);
  });

  it("an empty-metrics bucket with invoices reads the live readiness as 0 percent", () => {
    const b: RollupBucket = { ...bucket({ total: 4, ns: 4 }), metrics: {} };
    const v = view(
      run(addDays(TODAY, -1), 2, () => ({ total: 10, readiness_num: 5 })),
      b,
    );
    expect(v.trend?.now).toBe(0);
    expect(v.trend?.deltaLabel).toBe("▼ -50 pts since 27 Oct");
    noNaN(v);
  });

  it("the live not_submitted is draft plus validated and needs_attention is passed through", () => {
    const b = bucket({ total: 10, na: 7 });
    b.counts = { ...b.counts, draft: 2, validated: 3, accepted: 5 };
    const v = view(
      run(addDays(TODAY, -1), 2, () => ({
        not_submitted: 5,
        needs_attention: 7,
      })),
      b,
    );
    expect(v.sparks?.[3]).toBe(sparkPath([5, 5, 5]));
    expect(v.sparks?.[2]).toBe(sparkPath([7, 7, 7]));
  });

  it("the live total is the sum of all seven counts", () => {
    const b = bucket({ total: 10 });
    b.counts = {
      draft: 1,
      validated: 1,
      queued: 1,
      submitted: 1,
      accepted: 1,
      rejected: 1,
      failed: 1,
    };
    const v = view(
      run(addDays(TODAY, -1), 2, () => ({ total: 7 })),
      b,
    );
    expect(v.sparks?.[0]).toBe(sparkPath([7, 7, 7]));
  });

  it("84 and 85 recorded days both draw 84 days and start the label on the same day", () => {
    const a = view(run(addDays(TODAY, -1), 84), bucket());
    const b = view(run(addDays(TODAY, -1), 85), bucket());
    const first = addDays(TODAY, -83);
    expect(a.trend?.meta).toBe("12 WEEKS");
    expect(b.trend?.meta).toBe("12 WEEKS");
    expect(a.trend?.deltaLabel).toBe(b.trend?.deltaLabel);
    expect(b.trend?.deltaLabel).toContain(
      `since ${Number(first.slice(8))} Aug`,
    );
  });

  it("in-house sums per day across companies that record different days", () => {
    const rows = [
      row("2026-10-27", { entity_id: "A", total: 10, readiness_num: 10 }),
      row("2026-10-28", { entity_id: "A", total: 10, readiness_num: 10 }),
      row("2026-10-28", { entity_id: "B", total: 30, readiness_num: 0 }),
    ];
    const days = scopedDays(true, null, rows);
    expect(days).toHaveLength(2);
    expect(days[1]).toMatchObject({ total: 40, readiness_num: 10 });
    const v = view(
      rows,
      bucket({ total: 40, num: 10 }),
      "2026-10-28",
      true,
      null,
    );
    expect(v.trend?.line).toBe(trendPath([100, 25]).line);
  });

  it("firm rows given newest first are still drawn oldest first", () => {
    const rows = [
      row(TODAY, { total: 3 }),
      row("2026-10-28", { total: 2 }),
      row("2026-10-27", { total: 1 }),
    ];
    expect(view(rows, bucket({ total: 3 })).sparks?.[0]).toBe(
      sparkPath([1, 2, 3]),
    );
  });

  it("historyView with a bucket of metrics {} and a single recorded day is null, not a throw", () => {
    expect(view([row(TODAY)], EMPTY_BUCKET)).toEqual({
      sparks: null,
      trend: null,
    });
  });

  it("the sparks are drawn although the trend is null when too few days have a ratio", () => {
    const v = view(
      [row("2026-10-27", { total: 0 }), row("2026-10-28", { total: 5 })],
      bucket({ total: 0, num: 0, ns: 0 }),
    );
    expect(v.trend).toBeNull();
    expect(v.sparks).toHaveLength(4);
  });

  it("a month label exactly 0.1 after the previous one is kept", () => {
    // 11 days from 10-22: NOV at index 10 of 10 -> at 1; build 0.1 via 11 points, NOV at index 1
    const m = view(run("2026-11-10", 11), bucket(), "2026-11-10").trend?.months;
    expect(m?.map((x) => x.label)).toEqual(["OCT", "NOV"]);
    expect(m?.[1].at).toBeCloseTo(0.1, 10);
  });
});

describe("watToday", () => {
  it("watToday turns at 23:00 UTC", () => {
    expect(watToday(new Date("2026-10-20T22:59:59Z"))).toBe("2026-10-20");
    expect(watToday(new Date("2026-10-20T23:00:00Z"))).toBe("2026-10-21");
    expect(watToday(new Date("2026-12-31T23:00:00Z"))).toBe("2027-01-01");
  });
});
