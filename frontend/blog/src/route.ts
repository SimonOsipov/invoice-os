export type Route =
  | { view: 'home' }
  | { view: 'subscribe' }
  | { view: 'category'; id: string }
  | { view: 'post'; slug: string }

const HOME: Route = { view: 'home' }

// Segments are not decoded or checked against data; the page that renders them resolves them.
export function parseBlogPath(pathname: string): Route {
  const cat = /^\/category\/([^/]+)\/?$/.exec(pathname)
  if (cat) return { view: 'category', id: cat[1] }
  const one = /^\/([^/]+)\/?$/.exec(pathname)
  if (!one) return HOME
  if (one[1] === 'subscribe') return { view: 'subscribe' }
  if (one[1] === 'category') return HOME
  return { view: 'post', slug: one[1] }
}

export function blogPath(route: Route): string {
  switch (route.view) {
    case 'home':
      return '/'
    case 'subscribe':
      return '/subscribe'
    case 'category':
      return `/category/${route.id}`
    case 'post':
      return `/${route.slug}`
  }
}
