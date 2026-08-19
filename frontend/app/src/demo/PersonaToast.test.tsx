// @vitest-environment jsdom
// Explicit props, no ctx -- plain render(), matching PersonaPopover.test.tsx's idiom.
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { accessRoleLabel } from '../lib/members'
import { TOAST_DISMISS, TOAST_META, TOAST_TITLE } from './copy'
import { PersonaToast } from './PersonaToast'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('PersonaToast', () => {
  // Row (AC-3). Red today: no component.
  it('the toast names the person and the role', () => {
    render(<PersonaToast name="Musa Danjuma" initials="MD" role="reviewer" onDismiss={vi.fn()} />)

    expect(screen.getByTestId('persona-toast-title').textContent).toBe(TOAST_TITLE.replace('{full name}', 'Musa Danjuma'))
    expect(screen.getByTestId('persona-toast-meta').textContent).toBe(
      TOAST_META.replace('{ROLE}', accessRoleLabel('reviewer').toUpperCase()),
    )
    expect(screen.getByTestId('persona-toast').textContent).toContain('MD')
  })

  // Row (AC-3).
  it('the toast expires at 5.2s', () => {
    vi.useFakeTimers()
    const onDismiss = vi.fn()
    render(<PersonaToast name="Musa Danjuma" initials="MD" role="reviewer" onDismiss={onDismiss} />)

    act(() => {
      vi.advanceTimersByTime(5199)
    })
    expect(onDismiss).not.toHaveBeenCalled()

    act(() => {
      vi.advanceTimersByTime(2)
    })
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })

  // Row (AC-4). Replaces "raises no error and schedules no state update" -- React 19 makes
  // a post-unmount setState a silent no-op, so that assertion could never go red. An
  // uncleaned timer instead must never call onDismiss after unmount.
  it('unmounting mid-toast cancels the expiry', () => {
    vi.useFakeTimers()
    const onDismiss = vi.fn()
    const { unmount } = render(<PersonaToast name="Musa Danjuma" initials="MD" role="reviewer" onDismiss={onDismiss} />)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    unmount()

    act(() => {
      vi.advanceTimersByTime(6000)
    })
    expect(onDismiss).not.toHaveBeenCalled()
  })

  // Row (AC-3).
  it('the dismiss control is reachable by name and fires onDismiss', () => {
    const onDismiss = vi.fn()
    render(<PersonaToast name="Musa Danjuma" initials="MD" role="reviewer" onDismiss={onDismiss} />)

    fireEvent.click(screen.getByRole('button', { name: TOAST_DISMISS }))
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })
})
