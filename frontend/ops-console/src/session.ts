export const withSession = <T>(call: () => Promise<T>): Promise<T> => call()

export const expireOnce = (): void => undefined
