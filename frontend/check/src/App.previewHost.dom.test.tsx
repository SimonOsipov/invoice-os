// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://check-pr-1-944095690109.africa-south1.run.app/check/" }
import { describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('check on a preview host', () => {
  it('previewHost_loadsNoTag', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', 'G-TEST')
    const React = await import('react')
    const { createRoot } = await import('react-dom/client')
    const { App } = await import('./App')
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    React.act(() => root.render(React.createElement(App)))
    const accept = [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === 'Accept')!
    React.act(() => void accept.dispatchEvent(new MouseEvent('click', { bubbles: true })))
    expect(document.querySelectorAll('script[src*="googletagmanager"]')).toHaveLength(0)
    React.act(() => root.unmount())
  })
})
