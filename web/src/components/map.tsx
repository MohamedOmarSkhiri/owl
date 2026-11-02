// Map: one namespace as a graph, like Argo CD's application tree.
// The agent builds the graph (who owns, selects or uses what); this file lays it out left to
// right with dagre and draws it with React Flow (pan, zoom, minimap).
import { memo, useEffect, useMemo, useState, type ComponentType } from 'react'
import { useQuery } from '@tanstack/react-query'
import dagre from '@dagrejs/dagre'
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import {
  Box,
  Clock,
  Copy,
  Database,
  FileText,
  Gauge,
  Globe,
  HardDrive,
  KeyRound,
  Layers,
  Network,
  Play,
  Route,
  Server,
  type LucideProps,
} from 'lucide-react'
import { api, NAMESPACES, PODS, type MapHealth, type MapNode, type ResourceMap } from '../api'
import { getTheme } from '../theme'
import { ObjectPanel, PodPanel } from './panels'
import { NamespaceSelect } from './tabs'
import { ErrorNote, Loading } from './ui'

const REFRESH = 15_000
const NODE_W = 236
const NODE_H = 58

/** Icon and short label per kind, as Argo CD and kubectl abbreviate them. */
const KINDS: Record<string, { icon: ComponentType<LucideProps>; short: string }> = {
  Ingress: { icon: Globe, short: 'ing' },
  HTTPRoute: { icon: Route, short: 'route' },
  Service: { icon: Network, short: 'svc' },
  HorizontalPodAutoscaler: { icon: Gauge, short: 'hpa' },
  Deployment: { icon: Layers, short: 'deploy' },
  StatefulSet: { icon: Database, short: 'sts' },
  DaemonSet: { icon: Server, short: 'ds' },
  CronJob: { icon: Clock, short: 'cronjob' },
  ReplicaSet: { icon: Copy, short: 'rs' },
  Job: { icon: Play, short: 'job' },
  Pod: { icon: Box, short: 'pod' },
  ConfigMap: { icon: FileText, short: 'cm' },
  Secret: { icon: KeyRound, short: 'secret' },
  PersistentVolumeClaim: { icon: HardDrive, short: 'pvc' },
  PersistentVolume: { icon: HardDrive, short: 'pv' },
}
const CONFIG_KINDS = new Set(['ConfigMap', 'Secret', 'PersistentVolumeClaim', 'PersistentVolume'])

const HEALTH: Record<MapHealth, { color: string; label: string }> = {
  ok: { color: 'var(--color-ok)', label: 'Healthy' },
  warn: { color: 'var(--color-warn-soft)', label: 'Progressing' },
  bad: { color: 'var(--color-bad)', label: 'Failing' },
  none: { color: 'var(--color-line)', label: 'No health' },
}

type OwlNode = Node<{ item: MapNode }, 'owl'>

/** One box: a colored bar for health, the kind's icon, the name and its status. */
const ResourceNode = memo(function ResourceNode({ data }: NodeProps<OwlNode>) {
  const { item } = data
  const k = KINDS[item.kind] ?? { icon: Box, short: item.kind.toLowerCase() }
  const Icon = k.icon
  return (
    <div
      className={`flex h-[58px] w-[236px] items-center gap-2.5 overflow-hidden rounded-lg border bg-surface pr-3 shadow-sm transition-shadow hover:shadow-md ${
        item.missing ? 'border-dashed border-line' : 'border-line'
      } ${item.health === 'bad' ? 'ring-1 ring-bad/40' : ''}`}
      title={`${item.kind} ${item.name}${item.status ? `\n${item.status}` : ''}`}
    >
      <Handle type="target" position={Position.Left} className="!size-1.5 !border-0 !bg-line" />
      <span className="h-full w-1 shrink-0" style={{ background: HEALTH[item.health].color }} aria-hidden />
      <span className="grid size-8 shrink-0 place-items-center rounded-md bg-sunken text-muted">
        <Icon size={16} />
      </span>
      <span className="min-w-0 flex-1 leading-tight">
        <span className="flex items-baseline gap-1.5">
          <span className="shrink-0 font-mono text-[10px] text-muted uppercase">{k.short}</span>
          <span className="truncate text-[13px] font-medium text-ink">{item.name}</span>
        </span>
        {item.status && (
          <span className={`mt-0.5 block truncate text-[11px] ${item.health === 'bad' ? 'text-bad' : 'text-muted'}`}>
            {item.status}
          </span>
        )}
      </span>
      <Handle type="source" position={Position.Right} className="!size-1.5 !border-0 !bg-line" />
    </div>
  )
})
const nodeTypes = { owl: ResourceNode }

/** Left-to-right layers: entry points, then workloads, then their pods and what they use. */
function layout(map: ResourceMap, showPods: boolean, showConfig: boolean): { nodes: OwlNode[]; edges: Edge[] } {
  const keep = (n: MapNode) => (showPods || n.kind !== 'Pod') && (showConfig || !CONFIG_KINDS.has(n.kind))
  const items = map.nodes.filter(keep)
  const ids = new Set(items.map((n) => n.id))
  const links = map.edges.filter((e) => ids.has(e.from) && ids.has(e.to))

  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 14, ranksep: 64, marginx: 24, marginy: 24 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of items) g.setNode(n.id, { width: NODE_W, height: NODE_H })
  for (const e of links) g.setEdge(e.from, e.to)
  dagre.layout(g)

  const health = new Map(items.map((n) => [n.id, n.health]))
  return {
    nodes: items.map((item) => {
      const p = g.node(item.id)
      return { id: item.id, type: 'owl', position: { x: p.x - NODE_W / 2, y: p.y - NODE_H / 2 }, data: { item } }
    }),
    edges: links.map((e) => {
      const bad = health.get(e.to) === 'bad'
      return {
        id: `${e.from}>${e.to}`,
        source: e.from,
        target: e.to,
        type: 'default', // bezier: lines that share a column stay apart
        animated: bad, // draws the eye to the path that leads to a failure
        style: { stroke: bad ? 'var(--color-bad)' : 'var(--color-muted)', strokeWidth: 1.25, opacity: bad ? 0.9 : 0.55 },
      }
    }),
  }
}

/** The namespace with the most pods outside kube-system: a better first view than "default". */
function busiestNamespace(pods: { namespace?: string }[]): string | undefined {
  const count = new Map<string, number>()
  for (const p of pods) if (p.namespace && !p.namespace.startsWith('kube-')) count.set(p.namespace, (count.get(p.namespace) ?? 0) + 1)
  return [...count.entries()].sort((a, b) => b[1] - a[1])[0]?.[0]
}

function remembered(cluster: string): string {
  try {
    return localStorage.getItem(`owl.map.ns.${cluster}`) ?? ''
  } catch {
    return ''
  }
}

export default function MapTab({ cluster }: { cluster: string }) {
  const [ns, setNs] = useState(() => remembered(cluster))
  const pods = useQuery({ queryKey: ['list', cluster, 'pods', ''], queryFn: () => api.list(cluster, PODS), enabled: !ns })
  // Wait for the namespace list too, so a remembered namespace that was deleted is not kept.
  const namespaces = useQuery({ queryKey: ['list', cluster, 'namespaces'], queryFn: () => api.list(cluster, NAMESPACES) })
  useEffect(() => {
    if (!ns && pods.data) setNs(busiestNamespace(pods.data) ?? 'default')
  }, [ns, pods.data])
  useEffect(() => {
    if (ns && namespaces.data && !namespaces.data.some((n) => n.name === ns)) setNs('')
  }, [ns, namespaces.data])

  const choose = (v: string) => {
    setNs(v)
    try {
      localStorage.setItem(`owl.map.ns.${cluster}`, v)
    } catch {
      // not remembered, fine
    }
  }

  return (
    // React Flow needs a real height: the screen below the cluster header (taller on phones).
    <div className="flex h-[75dvh] min-h-[480px] flex-col md:h-[calc(100dvh-141px)]">
      {ns ? (
        <ReactFlowProvider>
          <NamespaceMap cluster={cluster} namespace={ns} onNamespace={choose} />
        </ReactFlowProvider>
      ) : (
        <Loading label="Choosing a namespace" />
      )}
    </div>
  )
}

function NamespaceMap({ cluster, namespace, onNamespace }: { cluster: string; namespace: string; onNamespace: (v: string) => void }) {
  const [showPods, setShowPods] = useState(true)
  const [showConfig, setShowConfig] = useState(true)
  const [open, setOpen] = useState<MapNode | null>(null)
  const flow = useReactFlow()
  const q = useQuery({ queryKey: ['map', cluster, namespace], queryFn: () => api.map(cluster, namespace), refetchInterval: REFRESH })

  // Lay out again only when the shape changes, so a refresh that only changes health does not move boxes.
  const shape = q.data ? `${q.data.nodes.map((n) => n.id).join()}|${q.data.edges.map((e) => e.from + e.to).join()}` : ''
  const laid = useMemo(
    () => (q.data ? layout(q.data, showPods, showConfig) : { nodes: [], edges: [] }),
    [shape, showPods, showConfig], // shape stands for q.data's structure on purpose
  )
  // Health and status do change on refresh: put the fresh values on the laid-out boxes.
  const fresh = useMemo(() => new Map((q.data?.nodes ?? []).map((n) => [n.id, n])), [q.data])
  const nodes = useMemo(
    () => laid.nodes.map((n) => ({ ...n, data: { item: fresh.get(n.id) ?? n.data.item } })),
    [laid.nodes, fresh],
  )
  // Fit the map, but never smaller than half size: readable first, pan for the rest.
  useEffect(() => {
    const t = setTimeout(() => flow.fitView({ padding: 0.08, minZoom: 0.5, maxZoom: 1, duration: 300 }), 50)
    return () => clearTimeout(t)
  }, [flow, namespace, showPods, showConfig, laid.nodes.length])

  const counts = { ok: 0, warn: 0, bad: 0, none: 0 }
  for (const n of q.data?.nodes ?? []) counts[n.health]++
  const theme = getTheme()

  return (
    <>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-3 border-b border-line px-5 py-3">
        <NamespaceSelect cluster={cluster} value={namespace} onChange={(v) => v && onNamespace(v)} />
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={showPods} onChange={(e) => setShowPods(e.target.checked)} className="size-4 accent-[var(--color-blue)]" />
          Pods
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={showConfig} onChange={(e) => setShowConfig(e.target.checked)} className="size-4 accent-[var(--color-blue)]" />
          Config and storage
        </label>
        <ul className="ml-auto flex flex-wrap items-center gap-4 text-xs text-muted" aria-label="Legend">
          {(['ok', 'warn', 'bad'] as const).map((h) => (
            <li key={h} className="flex items-center gap-1.5">
              <span className="size-2.5 rounded-sm" style={{ background: HEALTH[h].color }} aria-hidden />
              {HEALTH[h].label}
              <span className="tabular-nums text-ink">{counts[h]}</span>
            </li>
          ))}
        </ul>
      </div>
      {(q.data?.truncated || (q.data?.notes ?? []).length > 0) && (
        <div className="space-y-0.5 border-b border-line bg-sunken/50 px-5 py-2 text-xs text-muted">
          {q.data?.truncated && <p>Large namespace: the healthy pods past the first 400 resources are not shown.</p>}
          {q.data?.notes?.map((n) => <p key={n}>{n}</p>)}
        </div>
      )}
      <div className="relative min-h-0 flex-1">
        {q.isPending ? (
          <Loading />
        ) : q.isError ? (
          <ErrorNote error={q.error} onRetry={() => q.refetch()} />
        ) : q.data.nodes.length === 0 ? (
          <p className="px-6 py-16 text-center text-muted">Nothing to map in {namespace}.</p>
        ) : (
          <ReactFlow
            nodes={nodes}
            edges={laid.edges}
            nodeTypes={nodeTypes}
            colorMode={theme === 'auto' ? 'system' : theme}
            nodesDraggable={false}
            nodesConnectable={false}
            edgesFocusable={false}
            onNodeClick={(_, n) => !n.data.item.missing && setOpen(n.data.item)}
            minZoom={0.15}
            maxZoom={1.5}
            proOptions={{ hideAttribution: true }}
            className="owl-map"
            aria-label={`Resources in ${namespace}`}
          >
            <Background gap={18} size={1} />
            <Controls showInteractive={false} position="bottom-left" />
          </ReactFlow>
        )}
      </div>
      {open &&
        (open.kind === 'Pod' ? (
          <PodPanel cluster={cluster} namespace={namespace} name={open.name} onClose={() => setOpen(null)} />
        ) : (
          <ObjectPanel
            cluster={cluster}
            kind={{ group: open.group, version: open.version, resource: open.resource, kind: open.kind, namespaced: open.namespaced }}
            namespace={open.namespaced ? namespace : undefined}
            name={open.name}
            onClose={() => setOpen(null)}
          />
        ))}
    </>
  )
}
