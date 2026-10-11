import { capCodes, CODES_SHOWN, syncStamp } from '../rulesApi'
import type { CodeListSync } from '../types'
import { Drawer } from './Drawer'
import { isHeld, SyncChip } from './SyncStatus'

type Props = {
  list: string
  /** null while the read is in flight or failed. */
  syncs: CodeListSync[] | null
  note: string
  onClose: () => void
}

function Codes({ caption, codes, total }: { caption: string; codes: string[]; total: number }) {
  const { shown, more } = capCodes(codes, CODES_SHOWN, total)
  return (
    <div style={{ marginTop: 10 }}>
      <div className="label" style={{ marginBottom: 4 }}>
        {caption} · {total}
      </div>
      <div className="mono" style={{ fontSize: 11.5, color: 'var(--fg-2)', overflowWrap: 'anywhere', lineHeight: 1.5 }}>
        {total === 0 ? 'None' : shown.join(', ')}
        {more > 0 && <span style={{ color: 'var(--fg-3)' }}>{` and ${more} more`}</span>}
      </div>
    </div>
  )
}

export function CodeListDrawer({ list, syncs, note, onClose }: Props) {
  return (
    <Drawer
      width={580}
      onClose={onClose}
      header={
        <>
          <div className="eyebrow" style={{ marginBottom: 6 }}>
            CODE LIST
          </div>
          <div className="mono" style={{ fontSize: 15, fontWeight: 700, overflowWrap: 'anywhere' }}>
            {list}
          </div>
        </>
      }
    >
      {syncs === null && <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>{note}</div>}
      {syncs?.length === 0 && <div style={{ fontSize: 13, color: 'var(--fg-3)' }}>No sync recorded yet.</div>}
      {syncs?.map((s, i) => (
        <div key={i} style={{ border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', background: 'var(--bg-2)', padding: '12px 14px', marginBottom: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span className="mono" style={{ fontSize: 12.5, fontWeight: 600, flex: 1, minWidth: 0 }}>
              {syncStamp(s.synced_at)}
            </span>
            <SyncChip status={s.status} />
          </div>
          <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', marginTop: 2 }}>
            {s.entry_count} entries{isHeld(s.status) ? ' · held, not applied' : ''}
          </div>
          <Codes caption="Added" codes={s.added} total={s.added_count} />
          <Codes caption={isHeld(s.status) ? 'Would remove' : 'Removed'} codes={s.removed} total={s.removed_count} />
          <Codes caption="Changed" codes={s.changed} total={s.changed_count} />
        </div>
      ))}
    </Drawer>
  )
}
