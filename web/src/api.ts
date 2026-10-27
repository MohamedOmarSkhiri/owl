// Typed client for the Owl relay API (relay/cmd/relay). Same origin: the relay serves this app.

export type Health = 'healthy' | 'degraded' | 'down'

export interface Cluster {
  id: string
  name: string
  k8sVersion: string
  health: Health
  nodesTotal: number
  nodesReady: number
  podsTotal: number
  podsRunning: number
  podsFailing: number
  lastSeen: string
  // From relay 0.6.0 (missing on older relays, hence optional):
  podsCompleted?: number // finished Jobs: neither running nor failing
  problemsOpen?: number
  usage?: UsageTotals // whole-cluster CPU and memory; missing without metrics-server
}

export interface Problem {
  id: string
  cluster: string
  source: 'pod' | 'event' | 'alertmanager'
  severity: 'critical' | 'warning'
  namespace: string
  object: string
  reason: string
  message: string
  firstSeen: string
  lastSeen: string
  resolved: boolean
}

export interface ResourceKind {
  group: string
  version: string
  resource: string
  kind: string
  namespaced: boolean
}

export interface ResourceItem {
  name: string
  namespace?: string
  created: string
  status?: string
  healthy: boolean
  containers?: string[]
  lastRestart?: string // pods: when a container last restarted (absent = never)
}

export const PODS: ResourceKind = { group: '', version: 'v1', resource: 'pods', kind: 'Pod', namespaced: true }
export const NAMESPACES: ResourceKind = {
  group: '',
  version: 'v1',
  resource: 'namespaces',
  kind: 'Namespace',
  namespaced: false,
}

const TOKEN_KEY = 'owl.token'

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY)
  } catch {
    return null
  }
}

export function setToken(token: string | null) {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token)
    else localStorage.removeItem(TOKEN_KEY)
  } catch {
    // storage blocked (private mode): the token lives only in this tab
  }
  memoryToken = token
}

let memoryToken: string | null = null

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}

async function get<T>(path: string, query?: Record<string, string | undefined>, method = 'GET'): Promise<T> {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(query ?? {})) if (v) params.set(k, v)
  const url = params.size ? `${path}?${params}` : path
  const token = getToken() ?? memoryToken
  let res: Response
  try {
    res = await fetch(url, {
      method,
      headers: { Accept: 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    })
  } catch {
    throw new ApiError(0, 'Cannot reach the Owl relay. Check your connection.')
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = await res.json()
      if (typeof body?.error === 'string') msg = body.error
    } catch {
      // not JSON
    }
    if (res.status === 401) msg = 'This token was rejected by the relay.'
    throw new ApiError(res.status, msg)
  }
  return res.json() as Promise<T>
}

const c = (id: string) => encodeURIComponent(id)
const kindQuery = (k: ResourceKind) => ({ group: k.group, version: k.version, resource: k.resource })

export const api = {
  clusters: () => get<Cluster[]>('/v1/clusters'),
  problems: (id: string) => get<Problem[]>(`/v1/clusters/${c(id)}/problems`),
  kinds: (id: string) => get<ResourceKind[]>(`/v1/clusters/${c(id)}/kinds`),
  list: (id: string, kind: ResourceKind, namespace?: string) =>
    get<ResourceItem[]>(`/v1/clusters/${c(id)}/resources`, { ...kindQuery(kind), namespace }),
  get: (id: string, kind: ResourceKind, namespace: string | undefined, name: string) =>
    get<unknown>(`/v1/clusters/${c(id)}/resource`, { ...kindQuery(kind), namespace, name }),
  /** Without since: the last `tail` lines. With since (the previous answer's `last`): only newer lines. */
  logs: (id: string, namespace: string, pod: string, container?: string, tail: number | 'all' = 200, since?: string) =>
    get<{ lines: string[]; last?: string }>(`/v1/clusters/${c(id)}/pods/${c(namespace)}/${c(pod)}/logs`, {
      container,
      tail: String(tail),
      since,
    }),
  /** CPU and memory: per node (scope "nodes") or per pod (scope "pods", optionally one namespace). */
  usage: (id: string, scope: 'nodes' | 'pods', namespace?: string) =>
    get<UsageReport>(`/v1/clusters/${c(id)}/usage`, { scope, namespace }),
  /** CPU, memory, pods and problems over the last `minutes` (kept by the relay, at most 60 days). */
  history: (id: string, minutes: number) =>
    get<HistoryPoint[]>(`/v1/clusters/${c(id)}/history`, { minutes: String(minutes) }),
  info: () => get<{ version: string; persistent?: boolean }>('/v1/info'),
  /** Relay version, for the footer. */
  /** Which notification channels are on (secrets are never sent). */
  notifications: () => get<NotifyStatus>('/v1/notifications'),
  /** Sends a test message to every channel now; one result per channel. */
  testNotifications: () => get<NotifyChannel[]>('/v1/notifications/test', undefined, 'POST'),
  /** Validates a token without storing it. */
  check: async (token: string) => {
    const res = await fetch('/v1/clusters', { headers: { Authorization: `Bearer ${token}` } })
    if (res.status === 401) throw new ApiError(401, 'This token was rejected by the relay.')
    if (!res.ok) throw new ApiError(res.status, `The relay answered HTTP ${res.status}.`)
  },
}

export function timeAgo(iso: string | undefined): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (Number.isNaN(t) || t < Date.UTC(1971, 0)) return ''
  const s = Math.max(0, Math.round((Date.now() - t) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

/** Human plural for a kind, e.g. Deployment -> Deployments, Ingress -> Ingresses. */
export function kindLabel(k: ResourceKind): string {
  const special: Record<string, string> = { persistentvolumeclaims: 'PVCs', persistentvolumes: 'PVs', endpoints: 'Endpoints' }
  if (special[k.resource]) return special[k.resource]
  if (/(s|x|ch|sh)$/.test(k.kind)) return `${k.kind}es`
  if (/[^aeiou]y$/.test(k.kind)) return `${k.kind.slice(0, -1)}ies`
  return `${k.kind}s`
}

export const sameKind = (a: ResourceKind, b: ResourceKind) => a.group === b.group && a.resource === b.resource
// ---------- Resource usage (answered by the agent from metrics-server + Kubernetes) ----------
// CPU values are millicores (1000 = one core), memory values are bytes.

export interface NodeUsage {
  name: string
  ready: boolean
  pods: number
  cpuUsage: number
  memUsage: number
  cpuAllocatable: number // what pods may use on this node
  memAllocatable: number
  cpuRequests: number // sum of the requests of the pods placed on it
  memRequests: number
}

export interface PodUsage {
  name: string
  namespace: string
  node: string
  workloadKind: string // Deployment, StatefulSet, DaemonSet, Job, Pod...
  workloadName: string
  sampled: boolean // false: metrics-server has no sample yet (pod just started)
  cpuUsage: number
  memUsage: number
  cpuRequest: number
  cpuLimit: number // 0 = no limit
  memRequest: number
  memLimit: number
}

export interface UsageReport {
  metrics: boolean // false when metrics-server is missing: usage values are then 0
  message?: string
  nodes?: NodeUsage[]
  pods?: PodUsage[]
}

/** 250 -> "250m", 1500 -> "1.5 cores" */
export function fmtCPU(milli: number): string {
  if (milli < 1000) return `${Math.round(milli)}m`
  const cores = milli / 1000
  return `${cores >= 10 ? cores.toFixed(0) : cores.toFixed(1)} cores`
}

/** Binary units, like kubectl: 134217728 -> "128 Mi" */
export function fmtMem(bytes: number): string {
  const units = ['B', 'Ki', 'Mi', 'Gi', 'Ti']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}

// ---------- History (kept by the relay, one point per agent snapshot, rolled up with age) ----------

export interface UsageTotals {
  cpuUsage: number // millicores
  cpuAllocatable: number
  memUsage: number // bytes
  memAllocatable: number
}

export interface HistoryPoint {
  ts: string
  usage?: UsageTotals // average over the point's period; missing = no metrics then (gap in the chart)
  peak?: UsageTotals // highest value in the period (5-minute and hourly points only)
  running: number
  failing: number
  problems: number
}

/** Share of capacity in use, 0–100, or null when unknown. */
export function percent(used?: number, total?: number): number | null {
  return used === undefined || !total ? null : Math.round((used / total) * 100)
}
// ---------- Notifications (configured in the Helm chart, shown and tested in the app) ----------

export interface NotifyChannel {
  name: 'telegram' | 'slack' | 'discord' | 'webhook'
  target: string // e.g. "chat 42" or "hooks.slack.com/…": never the secret part
  ok?: boolean // test results only
  error?: string
}

export interface NotifyStatus {
  enabled: boolean
  channels: NotifyChannel[]
  minSeverity?: 'critical' | 'warning'
  resolved?: boolean
  cooldown?: string // Go duration, e.g. "10m0s"
  publicUrl?: string
}
