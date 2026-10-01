import * as Sentry from '@sentry/react'
import type { ReactNode } from 'react'
import { RecoveryScreen } from './RecoveryScreen'

export function CrashBoundary({ brand, children }: { brand: ReactNode; children: ReactNode }): ReactNode {
  return <Sentry.ErrorBoundary fallback={<RecoveryScreen brand={brand} />}>{children}</Sentry.ErrorBoundary>
}
