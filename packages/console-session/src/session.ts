export interface ConsoleSession {
  token: string
  refreshToken: string
}

export const CONSOLE_SESSION_SCHEMA_VERSION = 2

export function decodeJwtPayload(_token: string | null): Record<string, unknown> | null {
  return null
}

export function isStaffToken(_token: string | null): boolean {
  return false
}

export function parseStoredConsoleSession(_raw: string | null, _key: string): ConsoleSession | null {
  return null
}

export function loadConsoleSession(_key: string): ConsoleSession | null {
  return null
}

export function saveConsoleSession(_key: string, _s: ConsoleSession): void {}

export function clearConsoleSession(_key: string): void {}
