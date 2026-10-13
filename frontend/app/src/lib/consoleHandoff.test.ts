import { expect, it } from 'vitest'

import { consoleStartUrl } from './consoleHandoff'

it('consoleStartUrl_isTheConsoleAuthAppDoor', () => {
  expect(consoleStartUrl('https://sup.x')).toBe('https://sup.x/?auth=app')
})
