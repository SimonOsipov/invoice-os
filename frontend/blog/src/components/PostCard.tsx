import type { Category, Post } from '../data'
import { Icon } from '../icons'
import { fmtDate } from '../model'
import { Cover } from './Cover'

type PostCardProps = {
  post: Post
  cat: Category | undefined
  onOpen: () => void
  // A band card passes its band's border; a category card passes none and gets the pop-in and "Read the post".
  cardBd?: string
}

export function PostCard({ post, cat, onOpen, cardBd }: PostCardProps) {
  const band = cardBd !== undefined
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        background: 'var(--card)',
        border: `1px solid ${band ? cardBd : 'var(--border)'}`,
        borderRadius: 6,
        overflow: 'hidden',
        animation: band ? undefined : 'blogPop 320ms var(--ease-out)',
      }}
    >
      <div style={{ aspectRatio: '16 / 9', position: 'relative', background: 'var(--muted)' }}>
        <Cover post={post} cat={cat} />
      </div>
      <button
        type="button"
        onClick={onOpen}
        style={{
          flex: 1,
          textAlign: 'left',
          cursor: 'pointer',
          border: 0,
          background: 'transparent',
          color: 'var(--ink)',
          padding: '22px 24px 24px',
          display: 'flex',
          flexDirection: 'column',
          gap: 10,
        }}
      >
        <span className="mono" style={{ fontSize: 11, color: 'var(--muted-foreground)' }}>{fmtDate(post.publish_date)}</span>
        <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.01em', lineHeight: 1.3 }}>{post.title}</span>
        <span className="t-body-sm">{post.summary}</span>
        {!band && (
          <span
            style={{
              marginTop: 'auto',
              paddingTop: 6,
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              fontSize: 13,
              fontWeight: 700,
              color: 'var(--link)',
            }}
          >
            Read the post <Icon name="arrow-right" size={14} />
          </span>
        )}
      </button>
    </div>
  )
}
