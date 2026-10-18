/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { SceneView } from '../../library/src/components/SceneView'
import { DEMOS, toScene } from './demos'

const HERE = dirname(fileURLToPath(import.meta.url))
const html = (d: keyof typeof DEMOS, idx: number) =>
  renderToStaticMarkup(createElement(SceneView, { sc: toScene(DEMOS[d]), idx, size: 'inline' }))

describe('demos', () => {
  it('demos_idsAreTheServerDemos', () => {
    const go = readFileSync(join(HERE, '../../../internal/blog/blog.go'), 'utf8')
    const body = /Demos\s*=\s*\[\]string\{([^}]*)\}/.exec(go)![1]
    const ids = [...body.matchAll(/"([^"]+)"/g)].map((m) => m[1])
    expect(ids.length).toBeGreaterThan(0)
    expect(Object.keys(DEMOS)).toEqual(ids)
  })

  it('demos_holdThePrototypeSteps', () => {
    const imp = DEMOS.import
    if (imp.kind !== 'list') throw new Error('import is a list')
    expect(imp.steps).toHaveLength(4)
    expect(imp.show0).toBe(0)
    expect(imp.rows).toHaveLength(4)
    expect(imp.steps[2].set).toEqual({ 0: 'valid', 1: 'valid', 2: 'valid' })
    const app = DEMOS.approval
    if (app.kind !== 'list') throw new Error('approval is a list')
    expect(app.rows).toHaveLength(3)
    expect(app.steps).toHaveLength(4)
    const clr = DEMOS.clearance
    if (clr.kind !== 'flow') throw new Error('clearance is a flow')
    expect(clr.nodes).toEqual(['Approved', 'Prepared', 'Submitted', 'Response', 'Cleared'])
    expect(clr.steps.map((s) => s.at)).toEqual([1, 2, 3, 4])
  })

  it('toScene_flowNodesGainAnEmptySub', () => {
    const f = toScene(DEMOS.clearance)
    if (f.kind !== 'flow') throw new Error('flow')
    expect(f.nodes[0]).toEqual(['Approved', ''])
    expect('title' in f).toBe(false)
    const { title: _t, ...rest } = DEMOS.import
    expect(toScene(DEMOS.import)).toEqual(rest)
  })

  it('sceneView_inlineListUsesTheBlogPrototypeValues', () => {
    const h = html('import', 3)
    expect(h).toContain('font-size:12.5px;')
    expect(h).toContain('padding:10px')
    expect(h).toContain('font-size:9.5px')
    expect(h).toContain('font-size:10.5px')
    expect(h).toContain('class="mono" style="white-space:nowrap"')
    expect(h).not.toContain('class="mono" style="color')
    expect(h).toContain('transition:background 300ms;')
    expect(h).not.toContain('background 300ms ease-out')
    expect(html('import', 0)).not.toContain('animation')
  })

  it('sceneView_inlineFlowHasNoSubLine', () => {
    const h = html('clearance', 1)
    expect(h).toContain('width:34px')
    expect(h).not.toContain('font-size:11.5px')
    expect(h).toContain('font-size:13px')
    expect(h).not.toContain('min-height')
  })
})
