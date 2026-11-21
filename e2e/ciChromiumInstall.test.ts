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
function jobSteps(text: string): Map<string, string[]> {
  const jobs = new Map<string, string[]>()
  let inJobs = false
  let job: string | undefined
  for (const line of text.split('\n')) {
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

/** ids of jobs that run the seams step without an earlier install step. */
function offenders(text: string): string[] {
  return [...jobSteps(text)]
    .filter(([, steps]) => steps.includes(SEAMS))
    .filter(([, steps]) => !(steps.indexOf(INSTALL) >= 0 && steps.indexOf(INSTALL) < steps.indexOf(SEAMS)))
    .map(([id]) => id)
}

const job = (id: string, ...names: string[]) =>
  `  ${id}:\n    steps:\n${names.map((n) => `      - name: ${n}\n        run: x\n`).join('')}`

describe('ci.yml e2e seams', () => {
  it('every job that runs the e2e seams step installs Chromium first', () => {
    const jobs = jobSteps(readFileSync(CI_YML, 'utf8'))
    const withSeams = [...jobs].filter(([, steps]) => steps.includes(SEAMS))
    expect(withSeams.map(([id]) => id), 'found no job with the e2e seams step — the ci.yml scan is broken').toEqual(
      expect.arrayContaining(['frontend', 'library', 'blog']),
    )
    expect(offenders(readFileSync(CI_YML, 'utf8')), `jobs run "${SEAMS}" without an earlier "${INSTALL}" step`).toEqual([])
  })

  it('flags an install after the seams step, a commented-out install, an install in another job, and a near-miss name', () => {
    const yml = [
      'jobs:\n',
      job('after', SEAMS, INSTALL),
      job('commented', `${SEAMS}`).replace('    steps:\n', `    steps:\n      # - name: ${INSTALL}\n`),
      job('other', INSTALL),
      job('near', `${INSTALL} again`, SEAMS),
      job('ok', INSTALL, SEAMS),
      'other:\n',
      job('outside', SEAMS),
    ].join('')
    expect(offenders(yml)).toEqual(['after', 'commented', 'near'])
  })
})
