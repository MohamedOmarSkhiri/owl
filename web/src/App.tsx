import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Navigate, NavLink, Route, Routes, useParams } from 'react-router'
import { LogOut } from 'lucide-react'
import { api, ApiError, getToken, setToken, timeAgo, type Cluster } from './api'
import { ExploreTab, PodsTab, ProblemsTab } from './components/tabs'
import { ErrorNote, HealthDot, healthText, inputClass, Loading, OwlMark, Pulse } from './components/ui'

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

function SignIn({ onSignedIn }: { onSignedIn: () => void }) {
  const [token, setValue] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  const submit = async (e: { preventDefault(): void }) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await api.check(token.trim())
      setToken(token.trim())
      onSignedIn()
    } catch (err) {
      setError(err instanceof ApiError && err.status === 0 ? 'Cannot reach the Owl relay.' : (err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="flex min-h-full items-center justify-center p-6">
      <form onSubmit={submit} className="w-full max-w-sm">
        <OwlMark size={88} />
        <h1 className="mt-6 text-3xl font-semibold tracking-tight">Sign in to Owl</h1>
        <p className="mt-2 text-muted">Paste the app token of this relay. It is the value of RELAY_APP_TOKEN.</p>
        <label className="mt-8 block text-sm font-medium" htmlFor="token">
          App token
        </label>
        <input
          id="token"
          type="password"
          autoComplete="current-password"
          required
          value={token}
          onChange={(e) => setValue(e.target.value)}
          className={`${inputClass} mt-1.5 h-11 w-full`}
        />
        {error && (
          <p className="mt-3 text-sm text-bad" role="alert">
            {error}
          </p>
        )}
        <button
          type="submit"
          disabled={busy || !token.trim()}
          className="mt-6 h-11 w-full rounded-md bg-blue font-medium text-white hover:bg-blue-strong disabled:opacity-60"
        >
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}

function Shell({ onSignOut }: { onSignOut: () => void }) {
  const clusters = useQuery({ queryKey: ['clusters'], queryFn: api.clusters, refetchInterval: 15_000 })

  return (
    <div className="flex h-full flex-col md:flex-row">
      <aside className="flex shrink-0 flex-col border-b border-line bg-surface md:w-72 md:border-r md:border-b-0">
        <div className="flex items-center gap-2.5 px-5 py-4">
          <OwlMark size={34} />
          <span className="text-xl font-semibold tracking-tight">Owl</span>
          <button
            onClick={onSignOut}
            className="ml-auto rounded-md p-1.5 text-muted hover:bg-sunken hover:text-ink"
            aria-label="Sign out"
            title="Sign out"
          >
            <LogOut size={18} />
          </button>
        </div>
        <nav aria-label="Clusters" className="flex gap-1 overflow-x-auto px-3 pb-3 md:flex-col md:overflow-visible">
          {clusters.data?.map((c) => (
            <NavLink
              key={c.id}
              to={`/c/${encodeURIComponent(c.id)}`}
              className={({ isActive }) =>
                `flex min-w-44 items-center gap-3 rounded-lg px-3 py-2.5 md:min-w-0 ${
                  isActive ? 'bg-sunken text-ink' : 'text-muted hover:bg-sunken/60 hover:text-ink'
                }`
              }
            >
              <HealthDot health={c.health} />
              <span className="min-w-0">
                <span className="block truncate font-medium text-ink">{c.name}</span>
                <span className="block truncate text-xs">{c.k8sVersion}</span>
              </span>
            </NavLink>
          ))}
        </nav>
      </aside>
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
  if (clusters.length > 0) return <Navigate to={`/c/${encodeURIComponent(clusters[0].id)}`} replace />
  return (
    <div className="mx-auto max-w-xl px-6 py-20">
      <h1 className="text-2xl font-semibold">No clusters are reporting yet</h1>
      <p className="mt-3 text-muted">
        Install the Owl agent in a cluster and point it at this relay. The cluster appears here within 15 seconds.
      </p>
      <pre className="mt-6 overflow-x-auto rounded-lg bg-sunken p-4 font-mono text-sm">
        kubectl apply -f deploy/agent.yaml{'\n'}kubectl -n owl logs deploy/owl-agent -f
      </pre>
    </div>
  )
}

const TABS = [
  { key: 'problems', label: 'Problems' },
  { key: 'pods', label: 'Pods' },
  { key: 'explore', label: 'Explore' },
] as const

function ClusterView({ clusters }: { clusters: Cluster[] }) {
  const { id = '', tab = 'problems' } = useParams()
  const cluster = clusters.find((c) => c.id === id)
  if (!cluster) return <Navigate to="/" replace />
  const base = `/c/${encodeURIComponent(cluster.id)}`

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
          <dl className="flex gap-8">
            <Stat label="Nodes ready" value={`${cluster.nodesReady}/${cluster.nodesTotal}`} />
            <Stat label="Pods running" value={`${cluster.podsRunning}/${cluster.podsTotal}`} />
            <Stat label="Pods failing" value={String(cluster.podsFailing)} bad={cluster.podsFailing > 0} />
          </dl>
        </div>
        <div className="mt-3">
          <Pulse health={cluster.health} />
        </div>
        <nav className="-mb-px flex gap-6" aria-label="Cluster sections">
          {TABS.map((t) => (
            <NavLink
              key={t.key}
              to={`${base}/${t.key}`}
              className={() =>
                `border-b-2 pb-3 text-sm font-medium ${
                  tab === t.key ? 'border-blue text-ink' : 'border-transparent text-muted hover:text-ink'
                }`
              }
              aria-current={tab === t.key ? 'page' : undefined}
            >
              {t.label}
            </NavLink>
          ))}
        </nav>
      </header>
      <section className="flex-1 bg-surface">
        {tab === 'pods' ? (
          <PodsTab key={cluster.id} cluster={cluster.id} />
        ) : tab === 'explore' ? (
          <ExploreTab key={cluster.id} cluster={cluster.id} />
        ) : (
          <ProblemsTab key={cluster.id} cluster={cluster.id} />
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
