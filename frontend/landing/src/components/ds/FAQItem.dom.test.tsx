// @vitest-environment jsdom
// Interactive contract of FAQItem (jsdom). Source of truth: the DS FAQItem.jsx plus the
// story's deliberate divergences (controlled open/onToggle, answer stays mounted with `hidden`, chevron-down/up glyph swap,
// stroke 2). Glyph expectations come from GLYPHS.
import { createElement, useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GLYPHS } from '../../icons'
import { click, load, mountView, show, spyConsoleError, unmountView, type View } from './dsDom.test.util'

let view: View
let consoleError: ReturnType<typeof spyConsoleError>

beforeEach(() => {
  view = mountView()
  consoleError = spyConsoleError()
})

afterEach(() => {
  expect(consoleError).not.toHaveBeenCalled()
  unmountView(view)
  vi.restoreAllMocks()
})

async function mount(props: Record<string, unknown>) {
  const FAQItem = await load('FAQItem', 'FAQItem')
  await show(view, createElement(FAQItem, { question: 'Is it secure?', onToggle: () => undefined, ...props }, 'Answer body'))
  const buttons = view.container.querySelectorAll('button')
  expect(buttons, 'exactly one button').toHaveLength(1)
  return buttons[0] as HTMLButtonElement
}

const answerOf = (button: HTMLElement) => document.getElementById(button.getAttribute('aria-controls') as string)
const glyphOf = (button: HTMLElement) => Array.from(button.querySelectorAll('path')).map((p) => p.getAttribute('d'))

describe('FAQItem', () => {
  it('FQ-01 the question is a button whose aria-expanded follows open', async () => {
    const closed = await mount({ open: false })
    expect(closed.type).toBe('button')
    expect(closed.classList.contains('ds-faq-btn'), 'button has .ds-faq-btn').toBe(true)
    expect(closed.textContent).toBe('Is it secure?')
    expect(closed.getAttribute('aria-expanded')).toBe('false')
    const open = await mount({ open: true })
    expect(open.getAttribute('aria-expanded')).toBe('true')
    const root = view.container.firstElementChild as HTMLElement
    expect(Array.from(root.classList)).toEqual(['ds-faq'])
    expect(root.contains(open), 'button sits inside .ds-faq').toBe(true)
  })

  it('FQ-02 the answer is named by aria-controls and hidden while closed', async () => {
    const closed = await mount({ open: false })
    const answer = answerOf(closed)
    expect(closed.getAttribute('aria-controls'), 'aria-controls is set').toBeTruthy()
    expect(answer, 'aria-controls names a node even when closed').not.toBeNull()
    expect((answer as HTMLElement).hidden).toBe(true)
    expect((answer as HTMLElement).hasAttribute('hidden')).toBe(true)
    expect((answer as HTMLElement).classList.contains('ds-faq-a')).toBe(true)
    expect((answer as HTMLElement).textContent, 'answer stays mounted').toBe('Answer body')

    const open = await mount({ open: true })
    const shown = answerOf(open)
    expect(shown, 'same node named when open').not.toBeNull()
    expect((shown as HTMLElement).hasAttribute('hidden')).toBe(false)
    expect((shown as HTMLElement).textContent).toBe('Answer body')
    expect(open.contains(shown), 'the answer is a sibling of the button, not inside it').toBe(false)
  })

  it('FQ-03 a click toggles once', async () => {
    const onToggle = vi.fn()
    const button = await mount({ open: false, onToggle })
    expect(onToggle).not.toHaveBeenCalled()
    click(button)
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(button.getAttribute('aria-expanded'), 'controlled: open alone moves the state').toBe('false')
    click(button.querySelector('span') as HTMLElement)
    expect(onToggle, 'a click on the question text also toggles once').toHaveBeenCalledTimes(2)
  })

  it('FQ-04 the chevron follows the state', async () => {
    expect(GLYPHS['chevron-down'].length).toBeGreaterThan(0)
    expect(GLYPHS['chevron-down']).not.toEqual(GLYPHS['chevron-up'])
    const closed = await mount({ open: false })
    expect(glyphOf(closed)).toEqual([...GLYPHS['chevron-down']])
    const open = await mount({ open: true })
    expect(glyphOf(open)).toEqual([...GLYPHS['chevron-up']])
  })

  it('FQ-05 the chevron is one svg drawn at stroke 2', async () => {
    for (const open of [false, true]) {
      const button = await mount({ open })
      const svgs = button.querySelectorAll('svg')
      expect(svgs, `open=${open}: one svg`).toHaveLength(1)
      expect(svgs[0].getAttribute('stroke-width'), `open=${open}`).toBe('2')
      expect(svgs[0].getAttribute('width'), `open=${open}`).toBe('18')
    }
  })
})

describe('FAQItem toggling', () => {
  it('FQ-06 a question opens on one click and closes on the next', async () => {
    const FAQItem = await load('FAQItem', 'FAQItem')
    const spy = vi.fn()
    function Row() {
      const [open, setOpen] = useState(false)
      const onToggle = () => {
        spy()
        setOpen(!open)
      }
      return createElement(FAQItem, { question: 'Q', open, onToggle }, 'A')
    }
    await show(view, createElement(Row))
    const button = view.container.querySelector('button') as HTMLButtonElement
    expect(button).not.toBeNull()
    const state = () => ({ expanded: button.getAttribute('aria-expanded'), hidden: answerOf(button)?.hasAttribute('hidden'), glyph: glyphOf(button) })
    const closed = { expanded: 'false', hidden: true, glyph: [...GLYPHS['chevron-down']] }
    const open = { expanded: 'true', hidden: false, glyph: [...GLYPHS['chevron-up']] }
    expect(state()).toEqual(closed)
    click(button)
    expect(state()).toEqual(open)
    click(button)
    expect(state()).toEqual(closed)
    click(button)
    expect(state(), 'and opens again').toEqual(open)
    expect(spy).toHaveBeenCalledTimes(3)
  })

  it('FQ-07 two questions on one page never share an answer id', async () => {
    const FAQItem = await load('FAQItem', 'FAQItem')
    const item = (q: string) => createElement(FAQItem, { key: q, question: q, open: true, onToggle: () => undefined }, `${q} answer`)
    await show(view, createElement('div', null, item('One'), item('Two')))
    const buttons = Array.from(view.container.querySelectorAll('button'))
    expect(buttons).toHaveLength(2)
    const answers = buttons.map(answerOf)
    expect(answers[0], 'first button names a node').not.toBeNull()
    expect(answers[1], 'second button names a node').not.toBeNull()
    expect(answers[0]).not.toBe(answers[1])
    expect(answers.map((a) => a?.textContent)).toEqual(['One answer', 'Two answer'])
  })
})
