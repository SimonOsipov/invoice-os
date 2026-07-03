// Validation engine ported exactly from the prototype's `validate(d)` method
// (Platform.dc.html ~L1058-1081) — same rule order, same messages, same patches.

import type { Validatable, ValidationResult } from '../types'

export function validate(d: Validatable): ValidationResult {
  const errors: ValidationResult['errors'] = []
  const warnings: ValidationResult['warnings'] = []
  const passed: string[] = []

  if (/^\d{8}-\d{4}$/.test(d.buyerTin)) passed.push('Buyer TIN format · ' + d.buyerTin)
  else
    errors.push({
      id: 'tin',
      label: 'Buyer TIN format invalid',
      detail: 'EXPECTED ########-#### · GOT ' + (d.buyerTin || 'EMPTY'),
      fixLabel: 'Apply registry value',
      patch: { buyerTin: '19847720-0001' },
    })

  if (d.buyerAddress && d.buyerAddress.trim().length > 4) passed.push('Buyer billing address present')
  else
    errors.push({
      id: 'addr',
      label: 'Buyer billing address missing',
      detail: 'MANDATORY MBS FIELD',
      fixLabel: 'Pull from record',
      patch: { buyerAddress: '14 Apapa Road, Lagos, NG' },
    })

  passed.push('Mandatory seller fields present')
  passed.push('VAT computed at 7.5%')
  passed.push('Line totals reconcile to header')
  passed.push('Currency declared · NGN')
  passed.push('Invoice number unique in ledger')
  passed.push('Invoice date within open period')

  const hasServices = d.items.some((it) => /servic|consult|support|warehous|leasing/i.test(it.desc))
  if (hasServices && !d.wht)
    warnings.push({
      id: 'wht',
      label: 'WHT not applied on services line',
      detail: '5% WHT TYPICALLY APPLIES TO CONSULTING',
      fixLabel: 'Apply 5% WHT',
      patch: { wht: true },
    })
  else passed.push('Withholding-tax logic checks out')

  passed.push('Tax-point date valid')
  passed.push('Supplier VAT registration active')
  passed.push('Line HS / SKU codes present')
  passed.push('Rounding within ±0.01 tolerance')
  passed.push('Digital-signature slot reserved')
  passed.push('QR payload generated')
  passed.push('Document schema · UBL 2.1 valid')

  return { errors, warnings, passed }
}
