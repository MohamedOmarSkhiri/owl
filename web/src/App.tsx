import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Navigate, NavLink, Route, Routes, useLocation, useParams } from 'react-router'
import { api, getToken, percent, setToken, timeAgo, type Cluster } from './api'
import { ErrorNote, healthText, Loading, Pulse } from './components/ui'
import { ExploreTab, PodsTab, ProblemsTab } from './components/tabs'
import { UsageTab } from './components/usage'
import { OverviewTab } from './components/overview'
import { SECTIONS, Sidebar } from './components/sidebar'
import { SignIn } from './components/signin'

export default function App() {
  const [signedIn, setSignedIn] = useState(() => !!getToken())
  const client = useQueryClient()

  useEffect(() => {
    const out = () => {
      setToken(null)
      client.clear()
      setSignedIn(false)
    }
    window.addEventListener('owl:unauthorized', out)
    return () => window.removeEventListener('owl:unauthorized', out)
  }, [client])

  if (!signedIn) return <SignIn onSignedIn={() => setSignedIn(true)} />
  return (
    <Shell
      onSignOut={() => {
        setToken(null)
        client.clear()
        setSignedIn(false)
      }}
    />
  )
}

function Shell({ onSignOut }: { onSignOut: () => void }) {
  const clusters = useQuery({ queryKey: ['clusters'], queryFn: api.clusters, refetchInterval: 15_000 })
  // The sidebar sits outside <Routes>, so it reads the open cluster and section from the URL.
  const [, id, tab = 'overview'] = useLocation().pathname.match(/^\/c\/([^/]+)(?:\/([^/]+))?/) ?? []

  return (
    <div className="flex h-full flex-col md:flex-row">
      <Sidebar
        clusters={clusters.data ?? []}
        activeId={id && decodeURIComponent(id)}
        activeTab={tab}
        onSignOut={onSignOut}
      />
      <main className="min-h-0 min-w-0 flex-1 overflow-auto">
        {clusters.isPending ? (
          <Loading label="Loading clusters" />
        ) : clusters.isError ? (
          <ErrorNote error={clusters.error} onRetry={() => clusters.refetch()} />
        ) : (
          <Routes>
            <Route path="/" element={<Home clusters={clusters.data} />} />
            <Route path="/c/:id" element={<ClusterView clusters={clusters.data} />} />
            <Route path="/c/:id/:tab" element={<ClusterView clusters={clusters.data} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        )}
      </main>
    </div>
  )
}

function Home({ clusters }: { clusters: Cluster[] }) {
  if (clusters.length > 0) return <Navigate to={`/c/${encodeURIComponent(clusters[0].id)}/overview`} replace />
  return (
    <div className="mx-auto max-w-xl px-6 py-20">
      <h1 className="text-2xl font-semibold">No clusters are reporting yet</h1>
      <p className="mt-3 text-muted">
        Install the Owl agent in a cluster and point it at this relay. The cluster appears here within 15 seconds.
      </p>
      <pre className="mt-6 overflow-x-auto rounded-lg bg-sunken p-4 font-mono text-sm">
        kubectl -n owl get pods{'\n'}kubectl -n owl logs -l app.kubernetes.io/component=agent --tail=20
      </pre>
    </div>
  )
}

function ClusterView({ clusters }: { clusters: Cluster[] }) {
  const { id = '', tab = 'overview' } = useParams()
  const cluster = clusters.find((c) => c.id === id)
  if (!cluster) return <Navigate to="/" replace />
  const base = `/c/${encodeURIComponent(cluster.id)}`
  const u = cluster.usage
  const active = cluster.podsTotal - (cluster.podsCompleted ?? 0) // finished Jobs are not "not running"

  return (
    <div className="flex min-h-full flex-col">
      <header className="border-b border-line bg-surface px-5 pt-5">
        <div className="flex flex-wrap items-end gap-x-8 gap-y-3">
          <div className="min-w-0">
            <h1 className="truncate text-2xl font-semibold tracking-tight">{cluster.name}</h1>
            <p className="text-sm text-muted">
              {healthText[cluster.health]}, Kubernetes {cluster.k8sVersion}, seen {timeAgo(cluster.lastSeen)} ago
            </p>
          </div>
          <dl className="flex flex-wrap gap-x-8 gap-y-2">
            <Stat label="Nodes ready" value={`${cluster.nodesReady}/${cluster.nodesTotal}`} />
            <Stat label="Pods running" value={`${cluster.podsRunning}/${active}`} />
            <Stat label="Pods failing" value={String(cluster.podsFailing)} bad={cluster.podsFailing > 0} />
            {u && <Stat label="CPU used" value={`${percent(u.cpuUsage, u.cpuAllocatable)}%`} />}
            {u && <Stat label="Memory used" value={`${percent(u.memUsage, u.memAllocatable)}%`} />}
          </dl>
        </div>
        <div className="mt-3 pb-3 md:pb-4">
          <Pulse health={cluster.health} />
        </div>
        {/* On phones the sidebar has no room for sections, so they stay here as tabs. */}
        <nav className="-mb-px flex gap-6 overflow-x-auto md:hidden" aria-label="Cluster sections">
          {SECTIONS.map((s) => (
            <NavLink
              key={s.key}
              to={`${base}/${s.key}`}
              className={`shrink-0 border-b-2 pb-3 text-sm font-medium ${
                tab === s.key ? 'border-blue text-ink' : 'border-transparent text-muted hover:text-ink'
              }`}
              aria-current={tab === s.key ? 'page' : undefined}
            >
              {s.label}
            </NavLink>
          ))}
        </nav>
      </header>
      <section className="flex-1 bg-surface">
        {tab === 'problems' ? (
          <ProblemsTab key={cluster.id} cluster={cluster.id} />
        ) : tab === 'pods' ? (
          <PodsTab key={cluster.id} cluster={cluster.id} />
        ) : tab === 'explore' ? (
          <ExploreTab key={cluster.id} cluster={cluster.id} />
        ) : tab === 'usage' ? (
          <UsageTab key={cluster.id} cluster={cluster.id} />
        ) : (
          <OverviewTab key={cluster.id} cluster={cluster} />
        )}
      </section>
    </div>
  )
}

function Stat({ label, value, bad }: { label: string; value: string; bad?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-muted">{label}</dt>
      <dd className={`text-2xl font-semibold tabular-nums ${bad ? 'text-bad' : ''}`}>{value}</dd>
    </div>
  )
}