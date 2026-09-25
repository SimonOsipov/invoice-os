// @vitest-environment jsdom
import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Pager } from './Pager'

afterEach(cleanup)

const MIDDLE = { limit: 10, offset: 10, total: 30 }

function buttons(container: HTMLElement) {
  const all = Array.from(container.querySelectorAll('button'))
  const prev = all.find((b) => b.textContent?.includes('Previous'))
  const next = all.find((b) => b.textContent?.includes('Next'))
  if (!prev || !next) throw new Error('pager buttons not rendered')
  return { prev, next }
}

describe('Pager behaviour', () => {
  it('busy freezes both buttons on a middle page', () => {
    const onGo = vi.fn()
    const { container } = render(<Pager pagination={MIDDLE} busy onGo={onGo} />)
    const { prev, next } = buttons(container)

    expect(prev.disabled).toBe(true)
    expect(next.disabled).toBe(true)
    fireEvent.click(prev)
    fireEvent.click(next)
    expect(onGo).toHaveBeenCalledTimes(0)
  })

  it('idle pager: Previous goes to offset-limit, Next to offset+limit', () => {
    const onGo = vi.fn()
    const { container } = render(<Pager pagination={MIDDLE} busy={false} onGo={onGo} />)
    const { prev, next } = buttons(container)

    expect(prev.disabled).toBe(false)
    expect(next.disabled).toBe(false)
    fireEvent.click(prev)
    expect(onGo).toHaveBeenLastCalledWith(0)
    fireEvent.click(next)
    expect(onGo).toHaveBeenLastCalledWith(20)
    expect(onGo).toHaveBeenCalledTimes(2)
  })

  it('edges: first page disables Previous, last page disables Next', () => {
    const first = render(<Pager pagination={{ ...MIDDLE, offset: 0 }} busy={false} onGo={vi.fn()} />)
    const a = buttons(first.container)
    expect(a.prev.disabled).toBe(true)
    expect(a.next.disabled).toBe(false)
    cleanup()

    const last = render(<Pager pagination={{ ...MIDDLE, offset: 20 }} busy={false} onGo={vi.fn()} />)
    const b = buttons(last.container)
    expect(b.prev.disabled).toBe(false)
    expect(b.next.disabled).toBe(true)
  })

  it('two pagers each describe their buttons with their own reason', () => {
    const { container } = render(
      <>
        <Pager testId="a" reason="A-reason" pagination={MIDDLE} busy onGo={vi.fn()} />
        <Pager testId="b" reason="B-reason" pagination={MIDDLE} busy onGo={vi.fn()} />
      </>,
    )
    const pagerB = container.querySelector<HTMLElement>('[data-testid="b"]')
    expect(pagerB).not.toBeNull()
    const { prev, next } = buttons(pagerB!)

    for (const btn of [prev, next]) {
      const id = btn.getAttribute('aria-describedby')
      expect(id).toBeTruthy()
      expect(document.getElementById(id!)?.textContent).toBe('B-reason')
    }
  })
})
