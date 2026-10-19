import { useState } from 'react'
import type { Category, Post } from '../data'
import { Icon } from '../icons'
import { coverUrl, placeholderOf } from '../model'

type CoverProps = { post: Post; cat: Category | undefined }

// Fills the positioned box that holds it; a missing or failed cover shows the category's tone.
export function Cover({ post, cat }: CoverProps) {
  const [failed, setFailed] = useState<string | null>(null)
  if (post.cover && failed !== post.cover) {
    return (
      <img
        alt="Cover image"
        src={coverUrl(post.cover)}
        onError={() => setFailed(post.cover)}
        style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover' }}
      />
    )
  }
  const ph = placeholderOf(cat)
  return (
    <div
      style={{
        position: 'absolute',
        inset: 0,
        background: ph.bg,
        color: ph.fg,
        display: 'grid',
        placeItems: 'center',
        overflow: 'hidden',
      }}
    >
      <div className="hero-grid" style={{ position: 'absolute', inset: 0, opacity: 0.45 }} />
      <span
        style={{
          position: 'relative',
          width: 56,
          height: 56,
          borderRadius: 6,
          display: 'grid',
          placeItems: 'center',
          background: ph.tile,
        }}
      >
        <Icon name={ph.icon} size={24} />
      </span>
    </div>
  )
}
