// Settings — tabbed: ERP connectors (toggle connect/disconnect), API & webhooks
// (base URL, keys dimmed by sandbox mode, endpoints, webhooks), and signing &
// certificates. Ported from Platform.dc.html ~L847-951 + the settings slices of
// renderVals() (~L1397-1435).

import { API_BASE, API_KEYS, CERTS, CONNECTOR_DEFS, ENDPOINTS, SETTINGS_TABS, WEBHOOKS } from '../data'
import { copyGlyph, plusGlyph, shieldGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

function methodColor(m: 'POST' | 'GET'): { bg: string; color: string } {
  if (m === 'POST') return { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' }
  if (m === 'GET') return { bg: 'var(--accent-tint)', color: 'var(--accent)' }
  return { bg: 'var(--bg-3)', color: 'var(--fg-2)' }
}

export function SettingsView({ ctx }: { ctx: PlatformCtx }) {
  const { settingsTab, sandbox, connectors } = ctx
  const connCount = CONNECTOR_DEFS.filter((c) => connectors[c.id]).length

  return (
    <div style={{ padding: '30px 36px 56px', maxWidth: 1080 }}>
      <div style={{ marginBottom: 22 }}>
        <h1 style={{ fontSize: 26, fontWeight: 600, letterSpacing: '-0.025em', margin: '0 0 4px' }}>Settings</h1>
        <p style={{ fontSize: 14, color: 'var(--fg-3)', margin: 0 }}>Integrations, developer access, and signing certificates</p>
      </div>
      <div style={{ display: 'flex', gap: 26, borderBottom: '1px solid var(--line-1)', marginBottom: 24 }}>
        {SETTINGS_TABS.map((t) => {
          const a = settingsTab === t.id
          return (
            <button
              key={t.id}
              onClick={() => ctx.setSettingsTab(t.id)}
              className="pf-btn"
              style={{ border: 0, background: 'transparent', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: a ? 600 : 500, color: a ? 'var(--fg-1)' : 'var(--fg-3)', padding: '0 0 12px', borderBottom: `2px solid ${a ? 'var(--accent)' : 'transparent'}`, marginBottom: -1 }}
            >
              {t.label}
            </button>
          )
        })}
      </div>

      {/* Connectors */}
      {settingsTab === 'connectors' && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <p style={{ fontSize: 13.5, color: 'var(--fg-2)', margin: 0, maxWidth: 560, lineHeight: 1.55 }}>
              Sync invoices automatically from your ERP or accounting system. Documents are pulled, validated, and queued for transmission.
            </p>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', flex: 'none' }}>
              {connCount} / 6 CONNECTED
            </span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {CONNECTOR_DEFS.map((c) => {
              const on = !!connectors[c.id]
              return (
                <div key={c.id} style={{ display: 'flex', alignItems: 'center', gap: 15, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: '16px 18px' }}>
                  <span style={{ flex: 'none', width: 42, height: 42, borderRadius: 9, background: 'var(--slate-800)', color: '#fff', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 12, fontWeight: 700, letterSpacing: '0.02em' }}>{c.mono}</span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
                      <span style={{ fontSize: 14.5, fontWeight: 600 }}>{c.name}</span>
                      <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--fg-3)', border: '1px solid var(--line-2)', borderRadius: 3, padding: '1px 5px', letterSpacing: '0.06em' }}>{c.cat}</span>
                    </div>
                    <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 3 }}>
                      {on ? 'Synced 2 min ago' : 'No sync yet'}
                    </div>
                  </div>
                  <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: on ? 'var(--status-green-bg)' : 'var(--bg-3)', border: `1px solid ${on ? 'var(--status-green-border)' : 'var(--line-2)'}`, borderRadius: 999, padding: '4px 10px' }}>
                    <span style={{ width: 5, height: 5, borderRadius: 99, background: on ? 'var(--status-green-text)' : 'var(--fg-3)' }} />
                    <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: on ? 'var(--status-green-text)' : 'var(--fg-3)', letterSpacing: '0.04em' }}>{on ? 'CONNECTED' : 'NOT CONNECTED'}</span>
                  </span>
                  <button
                    onClick={() => ctx.toggleConnector(c.id)}
                    className="pf-btn"
                    style={{ flex: 'none', height: 34, padding: '0 16px', borderRadius: 6, border: `1px solid ${on ? 'var(--line-2)' : 'var(--accent)'}`, background: on ? 'transparent' : 'var(--accent)', color: on ? 'var(--fg-2)' : '#fff', cursor: 'pointer', fontFamily: 'var(--font-sans)', fontSize: 13, fontWeight: 500 }}
                  >
                    {on ? 'Disconnect' : 'Connect'}
                  </button>
                </div>
              )
            })}
          </div>
        </>
      )}

      {/* API & webhooks */}
      {settingsTab === 'api' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, padding: '18px 20px' }}>
            <div className="label" style={{ marginBottom: 10 }}>
              Base URL
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <code className="mono" style={{ flex: 1, fontSize: 13, color: 'var(--fg-1)', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 6, padding: '9px 12px' }}>{API_BASE}</code>
              <button className="pf-btn" style={{ flex: 'none', height: 36, padding: '0 12px', borderRadius: 6, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
                {copyGlyph} Copy
              </button>
            </div>
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>API keys</span>
            </div>
            {API_KEYS.map((k) => {
              const dim = k.env === 'LIVE' ? (sandbox ? 0.45 : 1) : sandbox ? 1 : 0.45
              return (
                <div key={k.env} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '15px 20px', borderBottom: '1px solid var(--line-1)', opacity: dim }}>
                  <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', background: k.envBg, borderRadius: 4, padding: '3px 8px' }}>
                    <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: k.envColor, letterSpacing: '0.06em' }}>{k.env}</span>
                  </span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <code className="mono" style={{ fontSize: 12.5, color: 'var(--fg-1)', display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{k.key}</code>
                    <div style={{ fontSize: 11.5, color: 'var(--fg-3)', marginTop: 3 }}>{k.note}</div>
                  </div>
                  <button className="pf-btn" style={{ flex: 'none', height: 32, padding: '0 11px', borderRadius: 6, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12.5 }}>
                    {copyGlyph} Copy
                  </button>
                </div>
              )
            })}
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)' }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>Endpoints</span>
            </div>
            {ENDPOINTS.map((e) => {
              const mc = methodColor(e.m)
              return (
                <div key={e.path} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '13px 20px', borderBottom: '1px solid var(--line-1)' }}>
                  <span style={{ flex: 'none', width: 48, textAlign: 'center', background: mc.bg, borderRadius: 4, padding: '3px 0' }}>
                    <span className="mono" style={{ fontSize: 10, fontWeight: 700, color: mc.color }}>{e.m}</span>
                  </span>
                  <code className="mono" style={{ flex: 'none', fontSize: 13, color: 'var(--fg-1)', fontWeight: 500 }}>{e.path}</code>
                  <span style={{ flex: 1, fontSize: 12.5, color: 'var(--fg-3)', textAlign: 'right' }}>{e.desc}</span>
                </div>
              )
            })}
          </div>
          <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span style={{ fontSize: 14, fontWeight: 600 }}>Webhooks</span>
              <button className="pf-btn" style={{ height: 30, padding: '0 11px', borderRadius: 6, border: '1px solid var(--line-2)', background: 'var(--bg-2)', color: 'var(--fg-2)', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 12.5 }}>
                {plusGlyph} Add endpoint
              </button>
            </div>
            {WEBHOOKS.map((w) => (
              <div key={w.event} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '13px 20px', borderBottom: '1px solid var(--line-1)' }}>
                <code className="mono" style={{ flex: 'none', fontSize: 12.5, fontWeight: 600, color: 'var(--accent)' }}>{w.event}</code>
                <code className="mono" style={{ flex: 1, minWidth: 0, fontSize: 12, color: 'var(--fg-3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{w.url}</code>
                <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 999, padding: '3px 9px' }}>
                  <span style={{ width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} />
                  <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--status-green-text)' }}>{w.st}</span>
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Signing & certificates */}
      {settingsTab === 'signing' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {CERTS.map((c) => (
            <div key={c.name} style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 10, overflow: 'hidden' }}>
              <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', gap: 11 }}>
                <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 8, background: 'var(--accent-tint)', color: 'var(--accent)', display: 'grid', placeItems: 'center' }}>{shieldGlyph}</span>
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 14.5, fontWeight: 600 }}>{c.name}</div>
                  <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 2 }}>
                    {c.cn}
                  </div>
                </div>
                <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6, background: 'var(--status-green-bg)', border: '1px solid var(--status-green-border)', borderRadius: 999, padding: '4px 10px' }}>
                  <span style={{ width: 5, height: 5, borderRadius: 99, background: 'var(--status-green-text)' }} />
                  <span className="mono" style={{ fontSize: 9, fontWeight: 600, color: 'var(--status-green-text)' }}>ACTIVE</span>
                </span>
              </div>
              <div style={{ padding: '16px 20px' }}>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, marginBottom: 16 }}>
                  <div>
                    <div className="label" style={{ marginBottom: 4 }}>
                      Issuer
                    </div>
                    <div style={{ fontSize: 12.5, color: 'var(--fg-1)' }}>{c.issuer}</div>
                  </div>
                  <div>
                    <div className="label" style={{ marginBottom: 4 }}>
                      Serial
                    </div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--fg-1)' }}>{c.serial}</div>
                  </div>
                  <div>
                    <div className="label" style={{ marginBottom: 4 }}>
                      Issued
                    </div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--fg-1)' }}>{c.issued}</div>
                  </div>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                  <span style={{ fontSize: 12, color: 'var(--fg-2)' }}>Expires {c.expires}</span>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{c.daysLeft} left</span>
                </div>
                <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 3, overflow: 'hidden', marginBottom: 16 }}>
                  <div style={{ width: c.pct, height: '100%', background: c.barColor, borderRadius: 3 }} />
                </div>
                <button className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
                  Renew certificate
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
