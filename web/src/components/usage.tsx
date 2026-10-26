// Usage tab: live CPU and memory per node, per workload and per pod,
// compared with node capacity, requests and limits.
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, fmtCPU, fmtMem, type NodeUsage, type PodUsage } from '../api'
import { PodPanel } from './panels'
import { NamespaceSelect, SearchBox } from './tabs'
import { ErrorNote, Loading } from './ui'

const REFRESH = 15_000 // metrics-server samples about every 15 s; polling faster shows nothing new

type View = 'nodes' | 'workloads' | 'pods'
type SortKey = 'cpu' | 'mem'

/** One horizontal meter: green below 70 %, amber below 90 %, red above. */
function Meter({ used, max, label }: { used: number; max: number; label: string }) {
  const ratio = max > 0 ? used / max : 0
  const pct = Math.round(ratio * 100)
  const color = ratio >= 0.9 ? 'var(--color-bad)' : ratio >= 0.7 ? 'var(--color-warn-soft)' : 'var(--color-ok)'
  return (
    <div className="min-w-36">
      <div className="flex justify-between gap-3 text-xs">
        <span className="tabular-nums">{label}</span>
        {max > 0 && <span className="text-muted tabular-nums">{pct}%</span>}
      </div>
      {max > 0 && (
        <div
          className="mt-1 h-1.5 overflow-hidden rounded-full bg-sunken"
          role="meter"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.min(pct, 100)}
          aria-label={label}
        >
          <div className="h-full rounded-full" style={{ width: `${Math.min(pct, 100)}%`, background: color }} />
        </div>
      )}
    </div>
  )
}

/** Usage against the limit if there is one, else against the request, else just the value. */
function PodMeter({ used, request, limit, fmt }: { used: number; request: number; limit: number; fmt: (n: number) => string }) {
  const max = limit || request
  const of = limit ? 'limit' : request ? 'request' : ''
  return <Meter used={used} max={max} label={of ? `${fmt(used)} of ${fmt(max)} ${of}` : fmt(used)} />
}

/** Header numbers for the cluster page: share of all node capacity in use. Hidden without metrics. */
export function ClusterUsageStats({ cluster }: { cluster: string }) {
  const q = useQuery({ queryKey: ['usage', cluster, 'nodes'], queryFn: () => api.usage(cluster, 'nodes'), refetchInterval: REFRESH })
  const nodes = q.data?.nodes ?? []
  if (!q.data?.metrics || nodes.length === 0) return null
  const sum = (f: (n: NodeUsage) => number) => nodes.reduce((t, n) => t + f(n), 0)
  const cpu = Math.round((sum((n) => n.cpuUsage) / Math.max(1, sum((n) => n.cpuAllocatable))) * 100)
  const mem = Math.round((sum((n) => n.memUsage) / Math.max(1, sum((n) => n.memAllocatable))) * 100)
  return (
    <>
      <div>
        <dt className="text-xs text-muted">CPU used</dt>
        <dd className={`text-2xl font-semibold tabular-nums ${cpu >= 90 ? 'text-bad' : ''}`}>{cpu}%</dd>
      </div>
      <div>
        <dt className="text-xs text-muted">Memory used</dt>
        <dd className={`text-2xl font-semibold tabular-nums ${mem >= 90 ? 'text-bad' : ''}`}>{mem}%</dd>
      </div>
    </>
  )
}

export function UsageTab({ cluster }: { cluster: string }) {
  const [view, setView] = useState<View>('nodes')
  const [ns, setNs] = useState('')
  const [text, setText] = useState('')
  const [sort, setSort] = useState<SortKey>('cpu')
  const [openPod, setOpenPod] = useState<PodUsage | null>(null)

  const nodes = useQuery({
    queryKey: ['usage', cluster, 'nodes'], // same key as the header: one request serves both
    queryFn: () => api.usage(cluster, 'nodes'),
    refetchInterval: REFRESH,
    enabled: view === 'nodes',
  })
  const pods = useQuery({
    queryKey: ['usage', cluster, 'pods', ns],
    queryFn: () => api.usage(cluster, 'pods', ns),
    refetchInterval: REFRESH,
    enabled: view !== 'nodes',
  })
  const active = view === 'nodes' ? nodes : pods

  // Workloads: add up the pods of each Deployment, StatefulSet, DaemonSet...
  const workloads = useMemo(() => {
    const groups = new Map<string, { kind: string; name: string; namespace: string; pods: PodUsage[] }>()
    for (const p of pods.data?.pods ?? []) {
      const key = `${p.namespace}/${p.workloadKind}/${p.workloadName}`
      const g = groups.get(key) ?? { kind: p.workloadKind, name: p.workloadName, namespace: p.namespace, pods: [] }
      g.pods.push(p)
      groups.set(key, g)
    }
    return [...groups.values()].map((g) => {
      const total = (f: (p: PodUsage) => number) => g.pods.reduce((t, p) => t + f(p), 0)
      const allLimited = (f: (p: PodUsage) => number) => g.pods.every((p) => f(p) > 0)
      return {
        ...g,
        cpuUsage: total((p) => p.cpuUsage),
        memUsage: total((p) => p.memUsage),
        cpuRequest: total((p) => p.cpuRequest),
        memRequest: total((p) => p.memRequest),
        cpuLimit: allLimited((p) => p.cpuLimit) ? total((p) => p.cpuLimit) : 0, // one unlimited pod = unlimited
        memLimit: allLimited((p) => p.memLimit) ? total((p) => p.memLimit) : 0,
      }
    })
  }, [pods.data])

  const t = text.trim().toLowerCase()
  const match = (...parts: string[]) => !t || parts.join(' ').toLowerCase().includes(t)
  const byUsage = <T extends { cpuUsage: number; memUsage: number }>(a: T, b: T) =>
    sort === 'cpu' ? b.cpuUsage - a.cpuUsage : b.memUsage - a.memUsage

  return (
    <>
      <div className="flex flex-wrap items-center gap-3 border-b border-line px-5 py-3">
        <div className="flex rounded-md border border-line p-0.5" role="tablist" aria-label="Group by">
          {(['nodes', 'workloads', 'pods'] as const).map((v) => (
            <button
              key={v}
              role="tab"
              aria-selected={view === v}
              onClick={() => setView(v)}
              className={`rounded px-3 py-1 text-sm capitalize ${view === v ? 'bg-blue text-white' : 'text-muted hover:text-ink'}`}
            >
              {v}
            </button>
          ))}
        </div>
        {view !== 'nodes' && <NamespaceSelect cluster={cluster} value={ns} onChange={setNs} />}
        <SearchBox value={text} onChange={setText} label={`Search ${view}`} />
        <label className="flex items-center gap-2 text-sm text-muted">
          Sort by
          <select
            value={sort}
            onChange={(e) => setSort(e.target.value as SortKey)}
            className="h-9 rounded-md border border-line bg-surface px-2 text-ink"
          >
            <option value="cpu">CPU</option>
            <option value="mem">Memory</option>
          </select>
        </label>
      </div>

      {active.data && !active.data.metrics && (
        <p className="mx-5 mt-4 rounded-lg bg-warn-soft/15 px-4 py-3 text-sm text-warn" role="status">
          {active.data.message} Requests, limits and capacity are still shown.
        </p>
      )}

      {active.isPending ? (
        <Loading label="Reading metrics" />
      ) : active.isError ? (
        <ErrorNote error={active.error} onRetry={() => active.refetch()} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-line text-left text-muted">
                <th className="px-4 py-2.5 font-medium">{view === 'nodes' ? 'Node' : view === 'workloads' ? 'Workload' : 'Pod'}</th>
                <th className="px-4 py-2.5 font-medium">CPU</th>
                <th className="px-4 py-2.5 font-medium">Memory</th>
                <th className="px-4 py-2.5 text-right font-medium">{view === 'pods' ? 'Node' : 'Pods'}</th>
              </tr>
            </thead>
            <tbody>
              {view === 'nodes' &&
                (nodes.data?.nodes ?? [])
                  .filter((n) => match(n.name))
                  .sort(byUsage)
                  .map((n) => (
                    <tr key={n.name} className="border-b border-line/70">
                      <td className="px-4 py-3">
                        <span className="font-medium">{n.name}</span>
                        <span className={`ml-2 text-xs ${n.ready ? 'text-ok' : 'text-bad'}`}>{n.ready ? 'Ready' : 'NotReady'}</span>
                        <span className="block text-xs text-muted">
                          requests {fmtCPU(n.cpuRequests)} CPU, {fmtMem(n.memRequests)} memory
                        </span>
                      </td>
                      <td className="px-4 py-3">
                        <Meter used={n.cpuUsage} max={n.cpuAllocatable} label={`${fmtCPU(n.cpuUsage)} of ${fmtCPU(n.cpuAllocatable)}`} />
                      </td>
                      <td className="px-4 py-3">
                        <Meter used={n.memUsage} max={n.memAllocatable} label={`${fmtMem(n.memUsage)} of ${fmtMem(n.memAllocatable)}`} />
                      </td>
                      <td className="px-4 py-3 text-right tabular-nums">{n.pods}</td>
                    </tr>
                  ))}

              {view === 'workloads' &&
                workloads
                  .filter((w) => match(w.namespace, w.name, w.kind))
                  .sort(byUsage)
                  .map((w) => (
                    <tr
                      key={`${w.namespace}/${w.kind}/${w.name}`}
                      className="cursor-pointer border-b border-line/70 hover:bg-sunken/70"
                      onClick={() => {
                        setView('pods') // drill down: show this workload's pods
                        setText(w.name)
                      }}
                    >
                      <td className="px-4 py-3">
                        <button className="font-medium hover:text-blue">{w.name}</button>
                        <span className="block text-xs text-muted">
                          {w.kind} in {w.namespace}
                        </span>
                      </td>
                      <td className="px-4 py-3">
                        <PodMeter used={w.cpuUsage} request={w.cpuRequest} limit={w.cpuLimit} fmt={fmtCPU} />
                      </td>
                      <td className="px-4 py-3">
                        <PodMeter used={w.memUsage} request={w.memRequest} limit={w.memLimit} fmt={fmtMem} />
                      </td>
                      <td className="px-4 py-3 text-right tabular-nums">{w.pods.length}</td>
                    </tr>
                  ))}

              {view === 'pods' &&
                (pods.data?.pods ?? [])
                  .filter((p) => match(p.namespace, p.name, p.workloadName))
                  .sort(byUsage)
                  .map((p) => (
                    <tr
                      key={`${p.namespace}/${p.name}`}
                      className="cursor-pointer border-b border-line/70 hover:bg-sunken/70"
                      onClick={() => setOpenPod(p)}
                    >
                      <td className="max-w-[26rem] px-4 py-3">
                        <button className="block max-w-full truncate text-left font-medium hover:text-blue">{p.name}</button>
                        <span className="block truncate text-xs text-muted">
                          {p.workloadKind} {p.workloadName} in {p.namespace}
                          {!p.sampled && pods.data?.metrics ? ', no sample yet' : ''}
                        </span>
                      </td>
                      <td className="px-4 py-3">
                        <PodMeter used={p.cpuUsage} request={p.cpuRequest} limit={p.cpuLimit} fmt={fmtCPU} />
                      </td>
                      <td className="px-4 py-3">
                        <PodMeter used={p.memUsage} request={p.memRequest} limit={p.memLimit} fmt={fmtMem} />
                      </td>
                      <td className="px-4 py-3 text-right text-muted">{p.node}</td>
                    </tr>
                  ))}
            </tbody>
          </table>
        </div>
      )}

      {openPod && (
        <PodPanel cluster={cluster} namespace={openPod.namespace} name={openPod.name} onClose={() => setOpenPod(null)} />
      )}
    </>
  )
}