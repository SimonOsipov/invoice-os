import { apiFetch, gatewayBase } from '@invoice-os/api-client/client'

// Wire values: internal/gateway blog_subscribe (weekly, every_post, monthly).
export type Frequency = 'weekly' | 'every_post' | 'monthly'

export type SubscribeBody = { email: string; category_ids: string[]; frequency: Frequency }

export async function subscribe(body: SubscribeBody): Promise<void> {
  const base = gatewayBase()
  if (base === null) throw new Error('blog: no gateway URL')
  await apiFetch<unknown>(`${base}/blog/subscriptions`, { method: 'POST', body })
}

export async function confirmSubscription(token: string): Promise<{ unsubscribedEverywhere: boolean }> {
  const base = gatewayBase()
  if (base === null) throw new Error('blog: no gateway URL')
  const res = await apiFetch<{ unsubscribed_everywhere?: unknown } | null>(`${base}/blog/subscriptions/confirm`, {
    method: 'POST',
    body: { token },
  })
  return { unsubscribedEverywhere: res?.unsubscribed_everywhere === true }
}
