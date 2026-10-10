import { GROUPS } from './content.ts'
import type { Feature, Group } from './types.ts'

export type Route =
  | { view: 'home' }
  | { view: 'group'; group: Group }
  | { view: 'feature'; group: Group; feature: Feature }

const HOME: Route = { view: 'home' }

export function parseLibraryPath(pathname: string): Route {
  const m = /^\/([^/]+)(?:\/([^/]+))?\/?$/.exec(pathname)
  if (!m) return HOME
  const group = GROUPS.find((g) => g.id === m[1])
  if (!group) return HOME
  if (m[2] === undefined) return { view: 'group', group }
  const feature = group.feats.find((f) => f.id === m[2])
  return feature ? { view: 'feature', group, feature } : HOME
}

export function libraryPath(route: Route): string {
  if (route.view === 'home') return '/'
  if (route.view === 'group') return `/${route.group.id}`
  return `/${route.group.id}/${route.feature.id}`
}
