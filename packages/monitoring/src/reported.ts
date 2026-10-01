const reported = new WeakSet<object>()

export function markReported(err: unknown): void {
  if ((typeof err === 'object' && err !== null) || typeof err === 'function') reported.add(err)
}

export function wasReported(err: unknown): boolean {
  return (typeof err === 'object' && err !== null) || typeof err === 'function' ? reported.has(err) : false
}
