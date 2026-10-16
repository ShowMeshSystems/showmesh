import { describe, expect, it } from 'vitest'
import type { PreviewTile } from '../api'
import { applyLayout, hideTile, layoutOf, moveStep, moveTo, sameLayout, showTile } from './previewLayoutModel'

const tile = (key: string, hidden = false): PreviewTile => ({ key, kind: 'ndi', label: key, state: 'live', message: '', hidden, observedAt: '2026-10-15T12:00:00Z' })

describe('preview layout model', () => {
  it('reads the layout a tile list shows', () => {
    expect(layoutOf([tile('a'), tile('b', true), tile('c')])).toEqual({ order: ['a', 'b', 'c'], hidden: ['b'] })
  })

  it('applies an order and hidden set, appending tiles the layout does not name', () => {
    const tiles = [tile('a'), tile('b'), tile('c'), tile('d')]
    const applied = applyLayout(tiles, { order: ['c', 'a'], hidden: ['a'] })
    expect(applied.map((t) => t.key)).toEqual(['c', 'a', 'b', 'd'])
    expect(applied.map((t) => t.hidden)).toEqual([false, true, false, false])
  })

  it('moves a key to an index, clamped to the ends', () => {
    const layout = { order: ['a', 'b', 'c'], hidden: [] }
    expect(moveTo(layout, 'a', 2).order).toEqual(['b', 'c', 'a'])
    expect(moveTo(layout, 'c', 0).order).toEqual(['c', 'a', 'b'])
    expect(moveTo(layout, 'a', 99).order).toEqual(['b', 'c', 'a'])
    expect(moveTo(layout, 'zzz', 0)).toBe(layout)
    expect(moveTo(layout, 'b', 1)).toBe(layout)
  })

  it('steps over hidden tiles and stops at the ends', () => {
    const layout = { order: ['a', 'b', 'c', 'd'], hidden: ['b'] }
    expect(moveStep(layout, 'c', -1).order).toEqual(['c', 'a', 'b', 'd'])
    expect(moveStep(layout, 'a', 1).order).toEqual(['b', 'c', 'a', 'd'])
    expect(moveStep(layout, 'a', -1)).toBe(layout)
    expect(moveStep(layout, 'd', 1)).toBe(layout)
  })

  it('hides and shows a tile without losing its place', () => {
    const layout = { order: ['a', 'b'], hidden: [] }
    const hidden = hideTile(layout, 'a')
    expect(hidden).toEqual({ order: ['a', 'b'], hidden: ['a'] })
    expect(hideTile(hidden, 'a')).toBe(hidden)
    expect(showTile(hidden, 'a')).toEqual(layout)
    expect(showTile(layout, 'a')).toBe(layout)
  })

  it('compares layouts by order and hidden set', () => {
    expect(sameLayout({ order: ['a', 'b'], hidden: ['a', 'b'] }, { order: ['a', 'b'], hidden: ['b', 'a'] })).toBe(true)
    expect(sameLayout({ order: ['a', 'b'], hidden: [] }, { order: ['b', 'a'], hidden: [] })).toBe(false)
  })
})
