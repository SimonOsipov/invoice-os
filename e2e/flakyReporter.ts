// A Playwright reporter that makes a retry-pass visible.
//
// Every config here sets `retries: CI ? 1 : 0`, so a spec that fails and then
// passes on its second attempt leaves the job GREEN. Until this reporter existed
// the only trace was one word in the run log — the HTML report was uploaded
// `if: failure()`, which by definition never fires on a retry-pass. Measured over
// the 7 most recent green Dev Env runs: 5 of them contained a retry-pass, across
// 6 distinct specs, none of it surfaced anywhere a reader would look.
//
// This reporter does not change what passes. It writes the flaky specs to the job
// summary and to stdout, so the next reader can judge whether each one is an
// intermittent product bug or a badly-written spec.
//
// Deliberately a Playwright reporter rather than a post-hoc JSON parser:
// Playwright compiles this TypeScript itself, so the deploy gate needs no extra
// runtime and no experimental Node flag.

import { appendFileSync } from 'node:fs'
import type { FullConfig, Reporter, Suite, TestCase } from '@playwright/test/reporter'

export type FlakySpec = {
  file: string
  line: number
  title: string
  attempts: number
}

/** The subset of Playwright's TestCase this reporter reads. */
export type TestLike = {
  outcome(): string
  titlePath(): string[]
  location?: { file: string; line: number }
  results?: unknown[]
}

/**
 * collectFlaky selects the tests Playwright itself judged flaky.
 *
 * `outcome()` is the authority — it is 'flaky' only when a test failed at least
 * once and then passed, which is exactly the case that reports green. Counting
 * `results.length > 1` instead would also catch a test that failed every attempt,
 * and that one already fails the job loudly.
 */
export function collectFlaky(tests: readonly TestLike[]): FlakySpec[] {
  const out: FlakySpec[] = []
  for (const t of tests) {
    if (t.outcome() !== 'flaky') continue
    const path = t.titlePath().filter(Boolean)
    out.push({
      file: t.location?.file ?? '<unknown>',
      line: t.location?.line ?? 0,
      title: path[path.length - 1] ?? '<untitled>',
      attempts: Array.isArray(t.results) ? t.results.length : 0,
    })
  }
  return out.sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line)
}

/** Trim an absolute path down to something readable in a job summary. */
export function shortenPath(file: string): string {
  const i = file.lastIndexOf('/e2e/')
  return i >= 0 ? file.slice(i + 5) : file
}

/**
 * formatFlakySummary renders GitHub-flavoured markdown for the job summary.
 * Returns '' when nothing flaked, so a clean run adds nothing to the summary.
 */
export function formatFlakySummary(label: string, flaky: readonly FlakySpec[]): string {
  if (flaky.length === 0) return ''
  const rows = flaky
    .map((f) => `| \`${shortenPath(f.file)}:${f.line}\` | ${f.title} | ${f.attempts} |`)
    .join('\n')
  const plural = flaky.length === 1 ? 'spec' : 'specs'
  return [
    `### ⚠️ ${label}: ${flaky.length} flaky ${plural} (passed only on retry)`,
    '',
    'These reported GREEN. Each is either an intermittent product bug or a spec that',
    'races the app — decide which, per spec. Silence here is what this table replaces.',
    '',
    '| Spec | Title | Attempts |',
    '| --- | --- | --- |',
    rows,
    '',
  ].join('\n')
}

export default class FlakyReporter implements Reporter {
  private readonly label: string
  private root: Suite | undefined

  constructor(options: { label?: string } = {}) {
    this.label = options.label ?? 'Playwright'
  }

  onBegin(_config: FullConfig, suite: Suite): void {
    // Kept for onEnd: FullResult carries no test list, and outcome() is only
    // final once every retry has run.
    this.root = suite
  }

  onEnd(): void {
    const tests: TestCase[] = this.root?.allTests() ?? []
    const flaky = collectFlaky(tests as unknown as TestLike[])
    const md = formatFlakySummary(this.label, flaky)
    if (!md) return

    // stdout so it survives even when no summary file exists (local runs).
    console.log(`\n${md}`)
    const summaryFile = process.env.GITHUB_STEP_SUMMARY
    if (summaryFile) {
      try {
        appendFileSync(summaryFile, `${md}\n`)
      } catch (err) {
        console.error(`flakyReporter: could not write the job summary: ${String(err)}`)
      }
    }
  }
}
