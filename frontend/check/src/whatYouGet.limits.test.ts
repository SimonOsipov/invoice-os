/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { MAX_INVOICES, MAX_RUNS_PER_EMAIL } from './copy'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../../..')
const goInt = (file: string, name: string): number => {
  const m = new RegExp(`\\b${name}\\s*=\\s*(\\d+)\\b`).exec(readFileSync(join(ROOT, file), 'utf8'))
  if (!m) throw new Error(`${name} not found`)
  return Number(m[1])
}

describe('limits follow the Go constants', () => {
  it('limits_maxInvoicesIsPublicCheckMaxInvoices', () => {
    expect(MAX_INVOICES).toBe(goInt('internal/publiccheck/check.go', 'MaxInvoices'))
  })

  it('limits_maxRunsIsReadinessCheckMaxRunsPerEmail', () => {
    expect(MAX_RUNS_PER_EMAIL).toBe(goInt('internal/readinesscheck/handlers.go', 'MaxRunsPerEmail'))
  })
})
