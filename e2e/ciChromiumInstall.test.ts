// BUG-170: apiTracker.test.ts launches Chromium, so every CI job that runs the
// e2e seams step needs the Chromium install step before it. library and blog had none.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const CI_YML = join(dirname(dirname(fileURLToPath(import.meta.url))), '.github/workflows/ci.yml')
const SEAMS = 'Unit tests (e2e seams)'
const INSTALL = 'Install Chromium (e2e seams)'

/** job id -> its step names in order (line scan: no yaml dependency is installed). */
function jobSteps(): Map<string, string[]> {
  const jobs = new Map<string, string[]>()
  let inJobs = false
  let job: string | undefined
  for (const line of readFileSync(CI_YML, 'utf8').split('\n')) {
    if (/^jobs:\s*$/.test(line)) inJobs = true
    else if (/^\S/.test(line)) inJobs = false
    if (!inJobs) continue
    const j = line.match(/^ {2}([\w-]+):\s*$/)
    if (j) {
      job = j[1]
      jobs.set(job, [])
    }
    const s = line.match(/^ {6}- name:\s*["']?(.+?)["']?\s*$/)
    if (s && job) jobs.get(job)!.push(s[1])
  }
  return jobs
}

describe('ci.yml e2e seams', () => {
  it('every job that runs the e2e seams step installs Chromium first', () => {
    const jobs = jobSteps()
    const withSeams = [...jobs].filter(([, steps]) => steps.includes(SEAMS))
    expect(withSeams.map(([id]) => id), 'found no job with the e2e seams step — the ci.yml scan is broken').toEqual(
      expect.arrayContaining(['frontend', 'library', 'blog']),
    )
    const offenders = withSeams
      .filter(([, steps]) => !(steps.indexOf(INSTALL) >= 0 && steps.indexOf(INSTALL) < steps.indexOf(SEAMS)))
      .map(([id]) => id)
    expect(offenders, `jobs run "${SEAMS}" without an earlier "${INSTALL}" step`).toEqual([])
  })
})
