import type { BlogData, Category } from '../data'
import { postsOf } from '../model'
import { PostCard } from './PostCard'

type CategoryPageProps = { data: BlogData; cat: Category; onPost: (slug: string) => void }

export function CategoryPage({ data, cat, onPost }: CategoryPageProps) {
  const posts = postsOf(data, cat.id)
  return (
    <section
      className="blog-px"
      style={{ padding: '56px 40px 80px', maxWidth: 1200, display: 'flex', flexDirection: 'column', gap: 40 }}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18, maxWidth: 700 }}>
        <div className="t-eyebrow">Category · {posts.length} posts</div>
        <h2 className="t-h2" style={{ margin: 0, fontSize: 44 }}>{cat.name}</h2>
        <p className="t-lead" style={{ margin: 0 }}>{cat.intro}</p>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: 20 }}>
        {posts.map((p) => (
          <PostCard key={p.id} post={p} cat={cat} onOpen={() => onPost(p.id)} />
        ))}
      </div>
    </section>
  )
}
