/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { APP_PATHS } from './appPaths.ts'
import type { ViewId } from './types.ts'

const ROUTE_TS = new URL('../../app/src/lib/route.ts', import.meta.url)

describe('appPaths', () => {
  it('appPaths_matchesRoutePathsInTheApp', () => {
    const src = readFileSync(ROUTE_TS, 'utf8').replace(/\/\*[\s\S]*?\*\/|\/\/.*/g, '')
    const block = /export const ROUTE_PATHS[^=]*=\s*\{([^}]*)\}/.exec(src)
    if (!block) throw new Error('route.ts: ROUTE_PATHS block not found')
    const entries = Object.fromEntries([...block[1].matchAll(/(\w+):\s*'([^']*)'/g)].map((m) => [m[1], m[2]]))
    if (Object.keys(entries).length < 11) throw new Error(`route.ts: ROUTE_PATHS has ${Object.keys(entries).length} entries, expected at least 11`)
    expect(entries.dashboard).toBe('/')
    const views = Object.keys(APP_PATHS) as ViewId[]
    expect(views).toHaveLength(11)
    for (const v of views) expect(APP_PATHS[v], v).toBe(entries[v])
  })
})
