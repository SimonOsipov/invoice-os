// Red stubs: the executor implements both (SENTRY-06-04, D-8, D-9, D-29).
export function countsAsIssue(_err: unknown, _signal?: AbortSignal): boolean {
  return false
}

export function reportApiFailure(_err: unknown, _req: { method: string; url: string; signal?: AbortSignal }): void {}
