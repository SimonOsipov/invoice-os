// @vitest-environment jsdom
import { useState } from 'react'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { CodeEntry } from '../lib/codeLists'
import { CodePicker } from './CodePicker'

const lagos: CodeEntry = { code: 'NG-LA', label: 'Lagos', percent: null }
const UNAVAILABLE = 'Codes are unavailable right now. Try again later.'

// Stateful parent: the label shows only once the parent feeds the picked code back as value.
function mount(fetch: ReturnType<typeof vi.fn>, value = '', over: Partial<React.ComponentProps<typeof CodePicker>> = {}) {
  const onChange = vi.fn()
  const onPick = vi.fn()
  function Parent() {
    const [v, setV] = useState(value)
    return (
      <CodePicker
        list="states"
        authedFetch={fetch as never}
        value={v}
        onChange={(c) => {
          onChange(c)
          setV(c)
        }}
        onPick={onPick}
        aria-label="State"
        {...over}
      />
    )
  }
  render(<Parent />)
  return { onChange, onPick, input: screen.getByLabelText('State') as HTMLInputElement }
}

const qOf = (fetch: ReturnType<typeof vi.fn>, call: number) => new URL(fetch.mock.calls[call][0]).searchParams.get('q')
const optionValues = () => Array.from(document.querySelectorAll('datalist option')).map((o) => (o as HTMLOptionElement).value)
const helperText = () => Array.from(document.querySelectorAll('span')).map((s) => s.textContent)
const flush = () => act(async () => {})

describe('CodePicker', () => {
  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
    vi.useFakeTimers()
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllEnvs()
    vi.restoreAllMocks()
  })

  it('codePicker_debouncesTheSearch', async () => {
    const fetch = vi.fn().mockResolvedValue({ codes: [] })
    const { input } = mount(fetch)
    fireEvent.change(input, { target: { value: 'La' } })
    await act(async () => void vi.advanceTimersByTime(100))
    fireEvent.change(input, { target: { value: 'Lag' } })
    await act(async () => void vi.advanceTimersByTime(250))
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(qOf(fetch, 0)).toBe('Lag')
  })

  it('codePicker_dropsAStaleAnswer', async () => {
    let resolveL!: (v: unknown) => void
    const fetch = vi.fn((url: string) => {
      const q = new URL(url).searchParams.get('q')
      if (q === 'L') return new Promise((r) => (resolveL = r))
      return Promise.resolve({ codes: [{ code: 'LA-1', label: 'La result', percent: null }] })
    })
    const { input } = mount(fetch)
    fireEvent.change(input, { target: { value: 'L' } })
    await act(async () => void vi.advanceTimersByTime(250))
    fireEvent.change(input, { target: { value: 'La' } })
    await act(async () => void vi.advanceTimersByTime(250))
    expect(optionValues()).toEqual(['LA-1'])
    await act(async () => resolveL({ codes: [{ code: 'L-OLD', label: 'old', percent: null }] }))
    expect(optionValues()).toEqual(['LA-1'])
  })

  it('codePicker_commitsOnExactMatch', async () => {
    const fetch = vi.fn().mockResolvedValue({ codes: [lagos] })
    const { input, onChange, onPick } = mount(fetch)
    fireEvent.change(input, { target: { value: 'NG-LA' } })
    await act(async () => void vi.advanceTimersByTime(250))
    expect(onChange).toHaveBeenCalledExactlyOnceWith('NG-LA')
    expect(onPick).toHaveBeenCalledExactlyOnceWith(lagos)
    expect(helperText()).toEqual(['Lagos'])
  })

  it('codePicker_revertsUnlistedTextOnBlur', async () => {
    const fetch = vi.fn().mockResolvedValue({ codes: [lagos] })
    const { input, onChange } = mount(fetch, 'NG-AB')
    fireEvent.change(input, { target: { value: 'Lagos' } })
    await act(async () => void vi.advanceTimersByTime(250))
    fireEvent.blur(input)
    expect(input.value).toBe('NG-AB')
    expect(onChange).not.toHaveBeenCalled()
  })

  it('codePicker_blankCommitsBlank', () => {
    const { input, onChange } = mount(vi.fn().mockResolvedValue({ codes: [] }), 'NG-AB')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.blur(input)
    expect(onChange).toHaveBeenCalledExactlyOnceWith('')
  })

  it('codePicker_failedSearchKeepsTheValue', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { input, onChange } = mount(vi.fn().mockRejectedValue(new Error('boom')), 'NG-AB')
    fireEvent.focus(input)
    await flush()
    expect(input.value).toBe('NG-AB')
    expect(screen.getByText(UNAVAILABLE)).toBeTruthy()
    expect(onChange).not.toHaveBeenCalled()
    expect(err).not.toHaveBeenCalled()
  })

  it('codePicker_emptyListSaysUnavailable', async () => {
    const fetch = vi.fn().mockResolvedValue({ codes: [] })
    const { input } = mount(fetch)
    fireEvent.focus(input)
    await flush()
    expect(qOf(fetch, 0)).toBe('')
    expect(screen.getByText(UNAVAILABLE)).toBeTruthy()
  })

  it('codePicker_noMatchSaysSo', async () => {
    const fetch = vi.fn((url: string) =>
      Promise.resolve({ codes: new URL(url).searchParams.get('q') === 'ZZQ' ? [] : [lagos] }),
    )
    const { input } = mount(fetch)
    fireEvent.change(input, { target: { value: 'ZZQ' } })
    await act(async () => void vi.advanceTimersByTime(250))
    expect(screen.getByText('No matching code.')).toBeTruthy()
    fireEvent.change(input, { target: { value: 'NG-LA' } })
    await act(async () => void vi.advanceTimersByTime(250))
    expect(screen.queryByText('No matching code.')).toBeNull()
    expect(helperText()).toEqual(['Lagos'])
  })
})
