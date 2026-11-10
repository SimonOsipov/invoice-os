/// <reference types="node" />
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { EXAMPLE_MAIL } from './copy'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '../../..')
const read = (p: string) => readFileSync(join(ROOT, p), 'utf8')
const found = (m: RegExpExecArray | null, what: string): string => {
  if (!m) throw new Error(`${what} not found`)
  return m[1]
}

// The last migration line (in name order) that holds the rule's tuple row: key, severity, then the message.
const lastTupleLine = (key: string): string => {
  const dir = join(ROOT, 'migrations')
  const row = new RegExp(`\\('${key}',.*('error'|'warning'|NULL), '`)
  let line: string | undefined
  for (const f of readdirSync(dir).filter((n) => n.endsWith('.sql')).sort()) {
    for (const l of readFileSync(join(dir, f), 'utf8').split('\n')) if (row.test(l)) line = l
  }
  if (!line) throw new Error(`no migration row for ${key}`)
  return line
}

describe('example mail follows the real mail', () => {
  it('exampleMail_subjectIsTheMailSubject', () => {
    const subject = found(/ReadinessCheckSubject\s*=\s*"([^"]+)"/.exec(read('internal/accountmail/accountmail.go')), 'ReadinessCheckSubject')
    expect(EXAMPLE_MAIL.subject).toBe(subject)
  })

  it('exampleMail_senderIsTheMailSender', () => {
    const from = found(/\bFrom\s*=\s*"[^"<]*<([^">]+)>"/.exec(read('internal/accountmail/sender.go')), 'From')
    expect(EXAMPLE_MAIL.from).toBe(from)
  })

  it('exampleMail_passLineIsTheTemplateLine', () => {
    expect(read('internal/accountmail/readiness_check.html')).toContain(EXAMPLE_MAIL.pass)
  })

  it('exampleMail_templateCarriesWarningsAndNotChecked', () => {
    const tpl = read('internal/accountmail/readiness_check.html')
    expect(tpl).toContain('>Warnings<')
    expect(tpl).toContain('>Not checked<')
  })

  it('exampleMail_ruleMessagesAreTheLastMigrationLiterals', () => {
    const [bad] = EXAMPLE_MAIL.invoices
    const messages = new Map(bad.rows.map((r) => [r.label, r.value]))
    expect(lastTupleLine('supplier-tin-required')).toContain(`'${messages.get('Supplier TIN')}'`)
    expect(lastTupleLine('buyer-tin-format')).toContain(`'${messages.get('Buyer TIN')}'`)
  })

  it('exampleMail_fieldLabelsAreTheFieldsGoLabels', () => {
    const fields = read('internal/invoicefields/fields.go')
    const [bad] = EXAMPLE_MAIL.invoices
    expect(bad.rows.map((r) => r.label)).toEqual(['Supplier TIN', 'Buyer TIN'])
    for (const [key, label] of [['supplier_tin', bad.rows[0].label], ['buyer_tin', bad.rows[1].label]]) {
      expect(fields, key).toContain(`Key: "${key}", Label: "${label}"`)
    }
  })
})
