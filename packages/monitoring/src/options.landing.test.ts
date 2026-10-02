import * as Sentry from '@sentry/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { sentryOptions, type Service } from './options'

vi.mock('@sentry/react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@sentry/react')>()
  return { ...actual, browserTracingIntegration: vi.fn(actual.browserTracingIntegration) }
})

const DSN = 'https://public@o1.ingest.de.sentry.io/1'
const routeName = (p: string): string => `R(${p})`
const tracing = vi.mocked(Sentry.browserTracingIntegration)

function build(service: string): void {
  expect(sentryOptions({ service: service as Service, dsn: DSN, release: 'r1', routeName }), `${service} options`).not.toBeNull()
}

beforeEach(() => tracing.mockClear())

describe('landing tracing options', () => {
  it('sentryOptions_landingTracesPageLoadsOnly', () => {
    build('landing')
    expect(tracing, 'landing builds its tracing once').toHaveBeenCalledTimes(1)
    const arg = tracing.mock.calls[0][0]!
    expect(arg).toEqual(expect.objectContaining({ instrumentNavigation: false, enableInp: false }))
    expect(typeof arg.beforeStartSpan, 'landing names spans').toBe('function')
    expect(arg.beforeStartSpan!({ name: '/privacy/' }).name, 'the span name comes from the route function').toBe('R(/privacy/)')

    tracing.mockClear()
    const bare = sentryOptions({ service: 'landing', dsn: DSN, release: 'r1' })!
    expect(tracing, 'control: landing without a route function still builds its tracing').toHaveBeenCalledTimes(1)
    expect(tracing.mock.calls[0][0]!.beforeStartSpan!({ name: '/privacy/NEEDLE-ID' }).name, 'no route function, no raw path').toBe('<unmatched>')
    expect(bare.tracesSampleRate).toBe(1)

    tracing.mockClear()
    build('app')
    expect(tracing, 'control: app builds its tracing once').toHaveBeenCalledTimes(1)
    const app = tracing.mock.calls[0][0]!
    expect(app.instrumentNavigation, 'app also skips the SDK navigation handler').toBe(false)
    expect(app.enableInp, 'control: app keeps INP').not.toBe(false)
  })
})
