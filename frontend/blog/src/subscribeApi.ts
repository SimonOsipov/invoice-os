import { apiFetch, gatewayBase } from '@invoice-os/api-client/client'

// Wire values: internal/gateway blog_subscribe (weekly, every_post, monthly).
export type Frequency = 'weekly' | 'every_post' | 'monthly'

export type SubscribeBody = { email: string; category_ids: string[]; frequency: Frequency }

export async function subscribe(body: SubscribeBody): Promise<void> {
  const base = gatewayBase()
  if (base === null) throw new Error('blog: no gateway URL')
  await apiFetch<unknown>(`${base}/blog/subscriptions`, { method: 'POST', body })
}
