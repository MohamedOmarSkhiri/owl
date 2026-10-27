import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp } from 'lucide-react'
import type { ResourceItem } from '../api'
import { timeAgo } from '../api'
import { Pill } from './ui'

/** Pod status from the agent: "CrashLoopBackOff · 0/1 · 12 restarts". */
function podParts(status = '') {
  const [state = '', ready = '', restarts = ''] = status.split(' · ')
  return { state, ready, restarts: Number(restarts.replace(' restarts', '')) || 0 }
}

const RECENT = 60 * 60 * 1000 // a restart in the last hour is worth highlighting; older ones are history

type Col = 'name' | 'namespace' | 'status' | 'ready' | 'restarts' | 'age'
type Sort = { col: Col; dir: 1 | -1 } | null

/** Value used to sort one column. Problems sort before healthy items. */
function sortValue(it: ResourceItem, col: Col): string | number {
  const p = podParts(it.status)
  switch (col) {
    case 'name':
      return it.name
    case 'namespace':
      return it.namespace ?? ''
    case 'status':
      return `${it.healthy ? 1 : 0}${p.state || it.status || ''}`
    case 'ready':
      return Number(p.ready.split('/')[0]) || 0
    case 'restarts':
      return p.restarts
    case 'age':
      return -new Date(it.created).getTime() // newest = smallest age
  }
}

export function ResourceTable({
  items,
  isPods,
  showNamespace,
  onOpen,
}: {
  items: ResourceItem[]
  isPods: boolean
  showNamespace: boolean
  onOpen: (item: ResourceItem) => void
}) {
  const [sort, setSort] = useState<Sort>(null) // null = the order the agent sent

  const rows = useMemo(() => {
    if (!sort) return items
    return [...items].sort((a, b) => {
      const x = sortValue(a, sort.col)
      const y = sortValue(b, sort.col)
      const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y))
      return c * sort.dir
    })
  }, [items, sort])

  // Click: sort ascending; again: descending; a third time: back to the original order.
  const toggle = (col: Col) =>
    setSort((s) => (s?.col !== col ? { col, dir: 1 } : s.dir === 1 ? { col, dir: -1 } : null))

  // A plain function, not a component: a component defined here would be re-created on every
  // render and the clicked button would lose keyboard focus.
  const th = (col: Col, label: string, right = false) => {
    const on = sort?.col === col
    return (
      <th
        key={col}
        className={`px-4 py-2.5 font-medium ${right ? 'text-right' : ''}`}
        aria-sort={on ? (sort.dir === 1 ? 'ascending' : 'descending') : 'none'}
      >
        <button
          onClick={() => toggle(col)}
          className={`inline-flex items-center gap-1 hover:text-ink ${on ? 'text-ink' : ''} ${right ? 'flex-row-reverse' : ''}`}
        >
          {label}
          {on && (sort.dir === 1 ? <ArrowUp size={13} /> : <ArrowDown size={13} />)}
        </button>
      </th>
    )
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-line text-left text-muted">
            {th('name', 'Name')}
            {showNamespace && th('namespace', 'Namespace')}
            {th('status', 'Status')}
            {isPods && th('ready', 'Ready', true)}
            {isPods && th('restarts', 'Restarts', true)}
            {th('age', 'Age', true)}
          </tr>
        </thead>
        <tbody>
          {rows.map((it) => {
            const p = isPods ? podParts(it.status) : null
            const completed = p?.state === 'Completed'
            const last = it.lastRestart ? new Date(it.lastRestart).getTime() : 0
            const recent = last > 0 && Date.now() - last < RECENT
            return (
              <tr
                key={`${it.namespace}/${it.name}`}
                className="cursor-pointer border-b border-line/70 hover:bg-sunken/70"
                onClick={() => onOpen(it)}
              >
                <td className="max-w-[28rem] px-4 py-2.5">
                  <button
                    className="block max-w-full truncate text-left font-medium text-ink hover:text-blue hover:underline"
                    onClick={(e) => {
                      e.stopPropagation()
                      onOpen(it)
                    }}
                  >
                    {it.name}
                  </button>
                </td>
                {showNamespace && <td className="px-4 py-2.5 text-muted">{it.namespace || '—'}</td>}
                <td className="px-4 py-2.5">
                  {p ? (
                    <Pill tone={completed ? 'plain' : it.healthy ? 'ok' : 'bad'}>{p.state}</Pill>
                  ) : it.status ? (
                    <Pill tone={it.healthy ? 'plain' : 'bad'}>{it.status.split(' · ').join('  ')}</Pill>
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                </td>
                {p && <td className="px-4 py-2.5 text-right tabular-nums">{completed ? '—' : p.ready}</td>}
                {p && (
                  <td className="px-4 py-2.5 text-right whitespace-nowrap tabular-nums">
                    {/* Like kubectl: "15 (8h ago)". Amber only when the last restart is recent. */}
                    <span className={recent ? 'font-medium text-warn' : p.restarts > 0 ? 'text-ink' : 'text-muted'}>
                      {p.restarts}
                    </span>
                    {last > 0 && <span className="ml-1 text-xs text-muted">({timeAgo(it.lastRestart!)} ago)</span>}
                  </td>
                )}
                <td className="px-4 py-2.5 text-right text-muted tabular-nums">{timeAgo(it.created)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}