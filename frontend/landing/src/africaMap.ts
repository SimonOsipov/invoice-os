export type MarkerId = 'NG' | 'KE' | 'ZA'
export type AfricaCountry = { id: string; name: string; d: string }
export type AfricaMap = { w: number; h: number; markers: Record<MarkerId, readonly [number, number]>; countries: readonly AfricaCountry[] }

export const AFRICA_MAP: AfricaMap = { w: 0, h: 0, markers: { NG: [0, 0], KE: [0, 0], ZA: [0, 0] }, countries: [] }
