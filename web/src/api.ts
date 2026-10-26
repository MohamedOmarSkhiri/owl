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

async function get<T>(path: string, query?: Record<string, string | undefined>): Promise<T> {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(query ?? {})) if (v) params.set(k, v)
  const url = params.size ? `${path}?${params}` : path
  const token = getToken() ?? memoryToken
  let res: Response
  try {
    res = await fetch(url, {
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
  logs: (id: string, namespace: string, pod: string, container?: string, tail = 200) =>
    get<{ lines: string[] }>(`/v1/clusters/${c(id)}/pods/${c(namespace)}/${c(pod)}/logs`, {
      container,
      tail: String(tail),
    }),
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
