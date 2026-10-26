import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertOctagon, AlertTriangle, BellRing, Search } from 'lucide-react'
import { api, kindLabel, NAMESPACES, PODS, sameKind, timeAgo, type Problem, type ResourceItem, type ResourceKind } from '../api'
import { ObjectPanel, PodPanel } from './panels'
import { ResourceTable } from './ResourceTable'
import { ErrorNote, inputClass, Loading, Panel } from './ui'

const REFRESH = 15_000

type Open = { kind: ResourceKind; namespace?: string; name: string } | null

function OpenPanel({ cluster, open, onClose }: { cluster: string; open: Open; onClose: () => void }) {
  if (!open) return null
  return sameKind(open.kind, PODS) ? (
    <PodPanel cluster={cluster} namespace={open.namespace ?? ''} name={open.name} onClose={onClose} />
  ) : (
    <ObjectPanel cluster={cluster} kind={open.kind} namespace={open.namespace} name={open.name} onClose={onClose} />
  )
}

export function NamespaceSelect({ cluster, value, onChange }: { cluster: string; value: string; onChange: (v: string) => void }) {
  const q = useQuery({ queryKey: ['list', cluster, 'namespaces'], queryFn: () => api.list(cluster, NAMESPACES) })
  const names = (q.data ?? []).map((n) => n.name).sort()
  return (
    <label className="flex items-center gap-2 text-sm text-muted">
      Namespace
      <select value={value} onChange={(e) => onChange(e.target.value)} className={`${inputClass} min-w-44`}>
        <option value="">All namespaces</option>
        {names.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </label>
  )
}

export function SearchBox({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  return (
    <label className="relative flex-1 sm:max-w-72">
      <span className="sr-only">{label}</span>
      <Search size={16} className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-muted" />
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={label}
        className={`${inputClass} w-full pl-8`}
      />
    </label>
  )
}

const severityIcon = {
  critical: <AlertOctagon size={18} className="text-bad" aria-label="Critical" />,
  warning: <AlertTriangle size={18} className="text-warn" aria-label="Warning" />,
}

export function ProblemsTab({ cluster }: { cluster: string }) {
  const [open, setOpen] = useState<Open>(null)
  const q = useQuery({ queryKey: ['problems', cluster], queryFn: () => api.problems(cluster), refetchInterval: REFRESH })

  const openProblem = (p: Problem) => {
    const [type, ...rest] = p.object.split('/')
    if (type === 'pod') setOpen({ kind: PODS, namespace: p.namespace, name: rest.join('/') })
  }

  if (q.isPending) return <Loading />
  if (q.isError) return <ErrorNote error={q.error} onRetry={() => q.refetch()} />
  if (q.data.length === 0)
    return <p className="px-6 py-12 text-center text-muted">Nothing needs your attention in this cluster right now.</p>

  return (
    <>
      <ul className="divide-y divide-line">
        {q.data.map((p) => {
          const isPod = p.object.startsWith('pod/')
          return (
            <li key={p.id}>
              <button
                onClick={() => openProblem(p)}
                disabled={!isPod}
                className="flex w-full items-start gap-3 px-5 py-3.5 text-left enabled:hover:bg-sunken/70"
              >
                <span className="mt-0.5">{p.source === 'alertmanager' ? <BellRing size={18} className="text-bad" /> : severityIcon[p.severity]}</span>
                <span className="min-w-0 flex-1">
                  <span className="flex flex-wrap items-baseline gap-x-3">
                    <span className="font-semibold">{p.reason}</span>
                    <span className="truncate text-sm text-muted">
                      {p.namespace ? `${p.namespace}/` : ''}
                      {p.object}
                    </span>
                  </span>
                  <span className="mt-0.5 line-clamp-2 block text-sm text-muted">{p.message}</span>
                </span>
                <span className="shrink-0 text-sm text-muted tabular-nums">{timeAgo(p.lastSeen)} ago</span>
              </button>
            </li>
          )
        })}
      </ul>
      <OpenPanel cluster={cluster} open={open} onClose={() => setOpen(null)} />
    </>
  )
}

function filterItems(items: ResourceItem[], text: string, problemsOnly = false) {
  const t = text.trim().toLowerCase()
  return items.filter(
    (i) => (!problemsOnly || !i.healthy) && (!t || `${i.namespace ?? ''}/${i.name}`.toLowerCase().includes(t)),
  )
}

export function PodsTab({ cluster }: { cluster: string }) {
  const [ns, setNs] = useState('')
  const [text, setText] = useState('')
  const [problemsOnly, setProblemsOnly] = useState(false)
  const [open, setOpen] = useState<Open>(null)
  const q = useQuery({
    queryKey: ['list', cluster, 'pods', ns],
    queryFn: () => api.list(cluster, PODS, ns),
    refetchInterval: REFRESH,
  })
  const shown = useMemo(() => filterItems(q.data ?? [], text, problemsOnly), [q.data, text, problemsOnly])
  const bad = (q.data ?? []).filter((p) => !p.healthy).length

  return (
    <>
      <div className="flex flex-wrap items-center gap-3 border-b border-line px-5 py-3">
        <NamespaceSelect cluster={cluster} value={ns} onChange={setNs} />
        <SearchBox value={text} onChange={setText} label="Search pods" />
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={problemsOnly}
            onChange={(e) => setProblemsOnly(e.target.checked)}
            className="size-4 accent-[var(--color-blue)]"
          />
          Only pods with problems
        </label>
        {q.data && (
          <span className="ml-auto text-sm text-muted">
            {q.data.length} pods{bad > 0 ? `, ${bad} with problems` : ''}
          </span>
        )}
      </div>
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorNote error={q.error} onRetry={() => q.refetch()} />
      ) : shown.length === 0 ? (
        <p className="px-6 py-12 text-center text-muted">No pods match.</p>
      ) : (
        <ResourceTable
          items={shown}
          isPods
          showNamespace={!ns}
          onOpen={(it) => setOpen({ kind: PODS, namespace: it.namespace, name: it.name })}
        />
      )}
      <OpenPanel cluster={cluster} open={open} onClose={() => setOpen(null)} />
    </>
  )
}

const QUICK = ['pods', 'deployments', 'statefulsets', 'daemonsets', 'services', 'ingresses', 'configmaps', 'persistentvolumeclaims', 'jobs', 'cronjobs']
const QUICK_GROUPS = ['', 'apps', 'batch', 'networking.k8s.io']

function KindPicker({ kinds, onPick, onClose }: { kinds: ResourceKind[]; onPick: (k: ResourceKind) => void; onClose: () => void }) {
  const [text, setText] = useState('')
  const t = text.trim().toLowerCase()
  const shown = kinds.filter((k) => !t || k.kind.toLowerCase().includes(t) || k.resource.includes(t) || k.group.includes(t))
  return (
    <Panel title="All resource types" subtitle={`${kinds.length} types, CRDs included`} onClose={onClose} wide={false}>
      <div className="sticky top-0 border-b border-line bg-surface px-5 py-3">
        <SearchBox value={text} onChange={setText} label="Find a type, e.g. route or certificate" />
      </div>
      <ul className="divide-y divide-line">
        {shown.map((k) => (
          <li key={`${k.group}/${k.resource}`}>
            <button onClick={() => onPick(k)} className="w-full px-5 py-2.5 text-left hover:bg-sunken/70">
              <span className="block font-medium">{k.kind}</span>
              <span className="block text-sm text-muted">
                {k.group ? `${k.group}/${k.version}` : k.version}
                {k.namespaced ? '' : ', cluster-wide'}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </Panel>
  )
}

export function ExploreTab({ cluster }: { cluster: string }) {
  const [ns, setNs] = useState('')
  const [kind, setKind] = useState<ResourceKind>(PODS)
  const [text, setText] = useState('')
  const [picking, setPicking] = useState(false)
  const [open, setOpen] = useState<Open>(null)

  const kinds = useQuery({ queryKey: ['kinds', cluster], queryFn: () => api.kinds(cluster), staleTime: 5 * 60_000 })
  const usable = useMemo(() => (kinds.data ?? []).filter((k) => !ns || k.namespaced), [kinds.data, ns])
  const quick = useMemo(() => {
    const out: ResourceKind[] = []
    for (const r of QUICK) {
      const k = usable.find((u) => u.resource === r && QUICK_GROUPS.includes(u.group))
      if (k) out.push(k)
    }
    if (!out.some((k) => sameKind(k, kind))) out.push(kind)
    return out
  }, [usable, kind])

  const list = useQuery({
    queryKey: ['list', cluster, kind.group, kind.resource, ns],
    queryFn: () => api.list(cluster, kind, ns),
    refetchInterval: REFRESH,
  })
  const shown = useMemo(() => filterItems(list.data ?? [], text), [list.data, text])

  const changeNs = (v: string) => {
    setNs(v)
    if (v && !kind.namespaced) setKind(PODS) // cluster-wide types have nothing inside a namespace
  }

  return (
    <>
      <div className="space-y-3 border-b border-line px-5 py-3">
        <div className="flex flex-wrap items-center gap-3">
          <NamespaceSelect cluster={cluster} value={ns} onChange={changeNs} />
          <SearchBox value={text} onChange={setText} label={`Search ${kindLabel(kind).toLowerCase()}`} />
        </div>
        <div className="flex flex-wrap gap-2" role="group" aria-label="Resource type">
          {quick.map((k) => {
            const on = sameKind(k, kind)
            return (
              <button
                key={`${k.group}/${k.resource}`}
                aria-pressed={on}
                onClick={() => setKind(k)}
                className={`rounded-full border px-3 py-1 text-sm ${
                  on ? 'border-blue bg-blue text-white' : 'border-line text-ink hover:border-blue hover:text-blue'
                }`}
              >
                {kindLabel(k)}
              </button>
            )
          })}
          <button
            onClick={() => setPicking(true)}
            disabled={!kinds.data}
            className="rounded-full border border-dashed border-line px-3 py-1 text-sm text-muted hover:border-blue hover:text-blue disabled:opacity-50"
          >
            All types…
          </button>
        </div>
      </div>
      {list.isPending ? (
        <Loading />
      ) : list.isError ? (
        <ErrorNote error={list.error} onRetry={() => list.refetch()} />
      ) : shown.length === 0 ? (
        <p className="px-6 py-12 text-center text-muted">
          No {kindLabel(kind).toLowerCase()} {ns ? `in ${ns}` : 'in this cluster'}.
        </p>
      ) : (
        <ResourceTable
          items={shown}
          isPods={sameKind(kind, PODS)}
          showNamespace={kind.namespaced && !ns}
          onOpen={(it) => setOpen({ kind, namespace: it.namespace || undefined, name: it.name })}
        />
      )}
      {picking && kinds.data && (
        <KindPicker
          kinds={usable}
          onPick={(k) => {
            setKind(k)
            setPicking(false)
          }}
          onClose={() => setPicking(false)}
        />
      )}
      <OpenPanel cluster={cluster} open={open} onClose={() => setOpen(null)} />
    </>
  )
}
