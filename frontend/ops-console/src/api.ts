export const request = <T>(_path: string, _opts: { method?: string; body?: unknown; signal?: AbortSignal } = {}): Promise<T> => Promise.resolve(undefined as T)
