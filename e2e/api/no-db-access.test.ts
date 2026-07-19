// M4-22-05 (task-163, Test Spec #5): the vitest-runnable half of "no direct DB
// access remains in the api suite" -- parallel in shape to what db.test.ts used to
// guard (deleted by this same subtask). RED right now: db.ts still imports pg and
// reads the superuser DSN env var directly (see its own file header) -- goes GREEN
// once the executor deletes db.ts in Stage 3. Deliberately NOT named db.test.ts,
// since that file is being deleted in the same stage.
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const API_DIR = dirname(fileURLToPath(import.meta.url))
const SELF = fileURLToPath(import.meta.url)

// Recursively lists every file under API_DIR, excluding this scanner's own file --
// otherwise the DSN-name check below would flag its own source for naming the very
// env var it exists to detect.
function listFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...listFiles(full))
    } else if (entry.isFile() && full !== SELF) {
      out.push(full)
    }
  }
  return out
}

const DSN_ENV_VAR = 'DATABASE_SUPERUSER_URL_DEV'
const PG_IMPORT = /from\s+['"]pg['"]|require\(\s*['"]pg['"]\s*\)/

describe('no direct DB access remains in the api suite', () => {
  const files = listFiles(API_DIR)

  it('has files to scan', () => {
    // Guards against a future refactor emptying the directory and the two
    // checks below passing vacuously.
    expect(files.length).toBeGreaterThan(0)
  })

  it('imports no pg', () => {
    const offenders = files.filter((f) => PG_IMPORT.test(readFileSync(f, 'utf8')))
    expect(offenders, `pg imported in: ${offenders.join(', ')}`).toEqual([])
  })

  it(`references no ${DSN_ENV_VAR}`, () => {
    const offenders = files.filter((f) => readFileSync(f, 'utf8').includes(DSN_ENV_VAR))
    expect(offenders, `${DSN_ENV_VAR} referenced in: ${offenders.join(', ')}`).toEqual([])
  })
})
