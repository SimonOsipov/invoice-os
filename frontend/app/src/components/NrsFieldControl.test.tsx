// @vitest-environment jsdom
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { NrsFieldControl } from './NrsFieldControl'

function mount(fieldKey: string, value = '') {
  const { container } = render(
    <NrsFieldControl fieldKey={fieldKey} value={value} onChange={() => {}} authedFetch={vi.fn() as never} aria-label="f" />,
  )
  return container
}

describe('NrsFieldControl', () => {
  afterEach(cleanup)

  it('nrsFieldControl_rendersByField', () => {
    expect(mount('buyer_state').querySelector('input[list]')).not.toBeNull()
    cleanup()

    const kind = mount('invoice_kind').querySelector('select')!
    expect(Array.from(kind.options).map((o) => o.text)).toEqual(['', 'B2B · Business', 'B2G · Government', 'B2C · Consumer'])
    cleanup()

    const status = mount('payment_status', 'Paid').querySelector('select')!
    expect(status.value).toBe('Paid')
    expect(Array.from(status.options).map((o) => o.value)).toEqual(['', 'Paid', 'PENDING', 'PAID', 'REJECTED', 'PARTIAL'])
    cleanup()

    for (const key of ['buyer_email', 'buyer_country', 'tax_currency_code']) {
      const c = mount(key)
      expect(c.querySelector('select')).toBeNull()
      const input = c.querySelector('input')!
      expect(input.hasAttribute('list')).toBe(false)
      expect(input.className).toBe('pf-input')
      cleanup()
    }
  })
})
