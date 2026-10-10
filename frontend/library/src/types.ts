export type ViewId = 'create' | 'invoices' | 'rules' | 'approvals' | 'workflows' | 'dashboard'
  | 'reports' | 'audit' | 'clients' | 'customers' | 'settings'
export type SceneKind = 'list' | 'form' | 'flow' | 'feed' | 'metrics' | 'toggles'
export type RowStatus = 'new' | 'valid' | 'draft' | 'cleared' | 'pending' | 'sent' | 'ready'
  | 'progress' | 'none' | 'approved' | 'error' | 'failed' | 'rejected'
export type Mark = 'ok' | 'low' | 'fix' | 'err'
export type Tone = 'ok' | 'info' | 'err'
export type FeedTag = 'g' | 'a' | 'm' | 'r'

export interface ListScene { kind: 'list'; win: string; cols: [string, string, string, string]; grid: string
  show0?: number; rows: [string, string, string, RowStatus][]
  steps: { cap: string; banner?: string; show?: number; focus?: number; set?: Record<number, RowStatus> }[] }
export interface FormScene { kind: 'form'; win: string; doc?: true
  fields: { l: string; v: string; fix?: string }[]
  steps: { cap: string; reveal?: number; focus?: number; mark?: Record<number, Mark>; msg?: string; tone?: Tone }[] }
export interface FlowScene { kind: 'flow'; win: string; nodes: [string, string][]
  steps: { cap: string; at: number; out: string }[] }
export interface FeedScene { kind: 'feed'; win: string; items: [string, string, string, FeedTag][]
  steps: { cap: string; show: number }[] }
export interface MetricsScene { kind: 'metrics'; win: string; tiles: [string, number, string][]; bars: number[]
  steps: { cap: string; grow: number }[] }
export interface TogglesScene { kind: 'toggles'; win: string; rows: [string, string, boolean][]
  steps: { cap: string; focus: number; flip?: Record<number, boolean> }[] }
export type Scene = ListScene | FormScene | FlowScene | FeedScene | MetricsScene | TogglesScene

interface FeatureBase { id: string; title: string; short: string; desc: string; benefits: string[]
  who: string[]; view: ViewId; rel: string[]; sc: Scene }
export type StatusFields = { status: 'shipped'; path: string } | { status: 'soon'; path?: never }
export type RawFeature = FeatureBase & StatusFields
export type Feature = RawFeature & { gid: string }
interface GroupBase { id: string; n: string; name: string; icon: string; view: ViewId
  one: string; intro: string }
export interface RawGroup extends GroupBase { feats: RawFeature[] }
export interface Group extends GroupBase { feats: Feature[] }
export type Stage = readonly [label: string, gid: string]
export interface TourStop { g: string; f: string; stage: number; t: string; d: string }
