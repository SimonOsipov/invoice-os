import { useEffect, useMemo, useState } from 'react'
import type { Category, Post } from '../data'
import { Icon } from '../icons'
import { demoHref } from '../links'
import { activeSection, fmtDate, scrollTargetTop, tocOf } from '../model'
import { Blocks } from './Blocks'
import { Button } from './Button'
import { Cover } from './Cover'
import { Toc } from './Toc'

type ArticleProps = {
  post: Post
  cat: Category | undefined
  onCategory: (id: string) => void
  onSubscribe: () => void
}

// #blog-main scrolls on a wide screen; on a phone the document does.
function scrollerOf(): HTMLElement | null {
  const main = document.getElementById('blog-main')
  if (main && main.scrollHeight > main.clientHeight) return main
  return document.scrollingElement as HTMLElement | null
}

const topOf = (s: HTMLElement) => (s.id === 'blog-main' ? s.getBoundingClientRect().top : 0)

export function Article({ post, cat, onCategory, onSubscribe }: ArticleProps) {
  const toc = useMemo(() => tocOf(post.body), [post.body])
  const [active, setActive] = useState(0)

  useEffect(() => {
    const onScroll = () => {
      const s = scrollerOf()
      if (!s) return
      const tops: number[] = []
      for (let i = 0; i < toc.length; i++) {
        const el = document.getElementById(`sec-${i}`)
        if (!el) break
        tops.push(el.getBoundingClientRect().top)
      }
      setActive(activeSection(tops, topOf(s) + 120))
    }
    const main = document.getElementById('blog-main')
    main?.addEventListener('scroll', onScroll)
    window.addEventListener('scroll', onScroll)
    return () => {
      main?.removeEventListener('scroll', onScroll)
      window.removeEventListener('scroll', onScroll)
    }
  }, [toc])

  const scrollToSection = (i: number) => {
    const s = scrollerOf()
    const el = document.getElementById(`sec-${i}`)
    if (!s || !el) return
    const header = document.getElementById('blog-header')
    const offset = s.id === 'blog-main' || !header ? 92 : header.getBoundingClientRect().height + 28
    s.scrollTo({ top: scrollTargetTop(s.scrollTop, el.getBoundingClientRect().top, topOf(s), offset), behavior: 'smooth' })
  }

  const meta = fmtDate(post.publish_date) + (post.author_name ? ` · ${post.author_name}` : '')

  return (
    <>
      <article
        className="blog-px"
        data-view="post"
        style={{ padding: '40px 40px 72px', display: 'flex', flexDirection: 'column', gap: 36 }}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
          {cat && (
            <button
              type="button"
              className="blog-back"
              onClick={() => onCategory(cat.id)}
              style={{
                alignSelf: 'flex-start',
                display: 'inline-flex',
                alignItems: 'center',
                gap: 6,
                background: 'none',
                border: 0,
                padding: 0,
                cursor: 'pointer',
                fontSize: 13,
                fontWeight: 700,
                color: 'var(--link)',
              }}
            >
              <Icon name="chevron-left" size={15} />
              <span>{cat.name}</span>
            </button>
          )}
          <span className="mono" style={{ fontSize: 12, color: 'var(--muted-foreground)' }}>{meta}</span>
          <h1
            style={{
              margin: 0,
              fontSize: 'clamp(36px, 4vw, 52px)',
              lineHeight: 1.1,
              letterSpacing: '-0.05em',
              fontWeight: 700,
              color: 'var(--ink)',
              textWrap: 'pretty',
            }}
          >
            {post.title}
          </h1>
          <p className="t-lead" style={{ margin: 0, maxWidth: 700, textWrap: 'pretty' }}>{post.summary}</p>
        </div>
        <div style={{ aspectRatio: '21 / 9', position: 'relative', background: 'var(--muted)', borderRadius: 10, overflow: 'hidden' }}>
          <Cover post={post} cat={cat} />
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', gap: 56, alignItems: 'flex-start' }}>
          <div style={{ flex: '1 1 520px', minWidth: 0, maxWidth: 700, display: 'flex', flexDirection: 'column', gap: 22 }}>
            <Blocks blocks={post.body} />
          </div>
          {toc.length > 1 && <Toc entries={toc} active={active} onSelect={scrollToSection} />}
        </div>
      </article>
      <section className="blog-px" style={{ background: 'var(--peach-band)', padding: '72px 40px' }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 32,
            flexWrap: 'wrap',
          }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxWidth: 620 }}>
            <div className="t-eyebrow">Book a demo</div>
            <h2 className="t-h2" style={{ margin: 0, fontSize: 38 }}>
              See it on your own invoices.
              <br />
              <span className="t-hl-peach">Start with a demo.</span>
            </h2>
          </div>
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <Button variant="primary" arrow href={demoHref()}>Book a demo</Button>
            <Button variant="outline" onClick={onSubscribe}>Subscribe</Button>
          </div>
        </div>
      </section>
    </>
  )
}
