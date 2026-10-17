import type { BlogFilters, ToastTone } from '../types'
import { BlogList } from './BlogList'

type Props = {
  filters: BlogFilters
  onFilters: (f: BlogFilters) => void
  onToast: (msg: string, tag?: string, tone?: ToastTone) => void
}

export function Blog(_props: Props) {
  return <BlogList sub="" />
}
