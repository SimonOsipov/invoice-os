type Props = {
  sub: string
}

export function BlogList({ sub }: Props) {
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24, flexWrap: 'wrap' }}>
        <div>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            07 — CONTENT
          </div>
          <h1 style={{ fontSize: 28, letterSpacing: '-0.04em', margin: 0 }}>Blog</h1>
          <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 6, letterSpacing: '0.03em' }}>
            {sub}
          </div>
        </div>
      </div>
    </div>
  )
}
