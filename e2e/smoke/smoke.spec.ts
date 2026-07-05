import { test, expect } from '@playwright/test'
import { APPS } from './apps'

// One smoke test per deployed SPA: the main mock view renders and the page logs
// no console errors or uncaught exceptions during load.
for (const app of APPS) {
  test(`${app.name}: main view renders with no console errors`, async ({ page }) => {
    const errors: string[] = []
    // Attach listeners before navigating so load-time errors are captured.
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text())
    })
    page.on('pageerror', (err) => {
      errors.push(`pageerror: ${err.message}`)
    })

    const response = await page.goto(app.url)
    expect(response, `no response from ${app.url}`).toBeTruthy()
    expect(response!.ok(), `${app.url} returned HTTP ${response!.status()}`).toBeTruthy()

    // Auto-waits for the signature element, so client-side render has completed
    // (and any load-time console errors have fired) by the time this resolves.
    await app.assertMainView(page)

    expect(errors, `console errors on ${app.name}:\n${errors.join('\n')}`).toEqual([])
  })
}
