import { describe, expect, it } from 'vitest'
import { progress } from './progress'

const states = (p: ReturnType<typeof progress>) => p.items.map((i) => i.state)

describe('progress', () => {
  it('progress_uploadWithNoFile', () => {
    const p = progress('upload', null)
    expect(states(p)).toEqual(['current', 'todo', 'todo', 'todo'])
    expect(p.items.map((i) => i.mark)).toEqual(['1', '2', '3', '4'])
    expect(p.phone).toEqual({ no: 1, total: 4, name: 'Upload', filled: 1 })
  })

  it('progress_uploadWhileReadingASpreadsheet', () => {
    const p = progress('upload', 'a.csv')
    expect(p.items[1].state).toBe('todo')
    expect(p.items[1].skipNote).toBeUndefined()
  })

  it('progress_mapMarksUploadDone', () => {
    const p = progress('map', 'a.xlsx')
    expect(states(p)).toEqual(['done', 'current', 'todo', 'todo'])
    expect(p.items[0].mark).toBe('✓')
    expect(p.phone).toMatchObject({ no: 2, total: 4, name: 'Match columns' })
  })

  it('progress_detailsIsTheEmailStep', () => {
    const p = progress('details', 'a.csv')
    expect(states(p)).toEqual(['done', 'done', 'current', 'todo'])
    expect(p.phone).toEqual({ no: 3, total: 4, name: 'Email', filled: 3 })
  })

  it('progress_aPdfSkipsMatchColumns', () => {
    const p = progress('details', 'INV-1.pdf')
    expect(p.items[1]).toMatchObject({ state: 'skipped', mark: '–', skipNote: 'Not needed for PDF' })
    expect(p.items[2].state).toBe('current')
    expect(p.phone).toMatchObject({ no: 2, total: 3, filled: 2 })
  })

  it('progress_aDocxNamesItsTypeInUpperCase', () => {
    expect(progress('details', 'Inv.Docx').items[1].skipNote).toBe('Not needed for DOCX')
  })

  it('progress_aSpreadsheetNeverSkips', () => {
    const p = progress('details', 'a.csv')
    expect(states(p)).not.toContain('skipped')
    expect(p.items.every((i) => i.skipNote === undefined)).toBe(true)
  })

  it('progress_sentIsAllDone', () => {
    const p = progress('sent', 'a.csv')
    expect(states(p)).toEqual(['done', 'done', 'done', 'done'])
    expect(p.phone).toMatchObject({ no: 4, total: 4, filled: 4 })
  })

  it('progress_sentAfterADocument', () => {
    const p = progress('sent', 'a.pdf')
    expect(states(p)).toEqual(['done', 'skipped', 'done', 'done'])
    expect(p.phone).toMatchObject({ no: 3, total: 3, filled: 3 })
  })
})
