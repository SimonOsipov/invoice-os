import { execFileSync } from 'node:child_process'

const GATEWAY = /^https:\/\/gateway-(pr-[1-9][0-9]*|nightly)-944095690109\.africa-south1\.run\.app\/?$/

// 'pr-<N>' or 'nightly', from GATEWAY_URL; the naming scripts/ci/preview.sh uses.
export function previewSuffix(): string {
  const url = process.env.GATEWAY_URL?.trim() ?? ''
  const m = GATEWAY.exec(url)
  if (!m) throw new Error(`preview.ts: GATEWAY_URL ${url} is not a preview gateway`)
  return m[1]
}

// One secret of this preview. The value stays in memory: never log or write it.
export function previewSecret(name: string): string {
  return execFileSync(
    'gcloud',
    ['secrets', 'versions', 'access', 'latest', '--secret', `${previewSuffix()}-${name}`, '--project', 'ascomply-preview'],
    { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] },
  )
}
