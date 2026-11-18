// BUG-146: a credential in a report trace failed the shared scrub step and dropped the flaky JSON,
// so fileFlakyBugs saw no retry-pass. The flaky JSON needs its own scrub, independent of the report's.
// No YAML parser is installed in e2e, so steps are split on their "- name:" lines.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const devEnv = readFileSync(join(REPO, '.github/workflows/dev-env.yml'), 'utf8')

interface Step {
  name: string
  id: string | undefined
  cond: string
  run: string
  path: string
}

function steps(job: string): Step[] {
  const all = devEnv.split('\n')
  const start = all.findIndex((l) => l === `  ${job}:`)
  if (start < 0) return []
  const rel = all.slice(start + 1).findIndex((l) => /^  [a-z0-9_-]+:$/.test(l))
  const body = all
    .slice(start, rel < 0 ? all.length : start + 1 + rel)
    .map((l) => l.replace(/(^|\s)#.*$/, '$1'))
  const out: string[][] = []
  for (const l of body) {
    if (/^ {6}- name:/.test(l)) out.push([l])
    else if (out.length) out[out.length - 1].push(l)
  }
  return out.map((chunk) => {
    const text = chunk.join('\n')
    const field = (re: RegExp) => text.match(re)?.[1]?.trim() ?? ''
    return {
      name: field(/- name:\s*(.+)/),
      id: field(/^ {8}id:\s*(.+)$/m) || undefined,
      cond: field(/^ {8}if:\s*(.+)$/m),
      run: field(/^ {8}run:\s*(.+)$/m),
      path: field(/^ {10}path:\s*(.+)$/m),
    }
  })
}

const ok = (id: string | undefined) => new RegExp(`steps\\.${id}\\.outcome == 'success'`)

describe.each(['e2e', 'topology'])('BUG-146 job %s: flaky JSON upload is independent of the report scrub', (job) => {
  const all = steps(job)
  const isScrub = (s: Step) => /reportScrub/.test(s.run)
  const reportScrubs = all.filter((s) => isScrub(s) && /e2e\/report-/.test(s.run))
  const flakyScrubs = all.filter((s) => isScrub(s) && /e2e\/flaky-results/.test(s.run) && !/e2e\/report-/.test(s.run))
  const flakyUpload = all.filter((s) => s.name === 'Upload the flaky JSON')
  const reportUploads = all.filter((s) => /^e2e\/report-/.test(s.path))

  it('parses the job', () => {
    expect(all.length).toBeGreaterThan(5)
    expect(flakyUpload).toHaveLength(1)
    expect(reportScrubs).toHaveLength(1)
    expect(reportUploads.length).toBeGreaterThan(0)
  })

  it('the flaky upload does not depend on the report scrub', () => {
    const reportId = reportScrubs[0]?.id
    expect(reportId).toBeTruthy()
    expect(flakyUpload[0].cond).not.toContain(`steps.${reportId}.`)
  })

  it('a separate scrub covers the flaky JSON only, and gates the flaky upload', () => {
    expect(flakyScrubs).toHaveLength(1)
    expect(flakyScrubs[0].id).toBeTruthy()
    expect(flakyScrubs[0].id).not.toBe(reportScrubs[0].id)
    expect(flakyUpload[0].cond).toMatch(ok(flakyScrubs[0].id))
  })

  it('the report uploads still require the report scrub to succeed', () => {
    for (const u of reportUploads) expect(u.cond, u.name).toMatch(ok(reportScrubs[0].id))
  })
})
