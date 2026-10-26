import type { ResourceItem } from '../api'
import { timeAgo } from '../api'
import { Pill } from './ui'

/** Pod status from the agent: "CrashLoopBackOff · 0/1 · 12 restarts". */
function podParts(status = '') {
  const [state = '', ready = '', restarts = ''] = status.split(' · ')
  return { state, ready, restarts: restarts.replace(' restarts', '') }
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
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-line text-left text-muted">
            <th className="px-4 py-2.5 font-medium">Name</th>
            {showNamespace && <th className="px-4 py-2.5 font-medium">Namespace</th>}
            <th className="px-4 py-2.5 font-medium">Status</th>
            {isPods && <th className="px-4 py-2.5 text-right font-medium">Ready</th>}
            {isPods && <th className="px-4 py-2.5 text-right font-medium">Restarts</th>}
            <th className="px-4 py-2.5 text-right font-medium">Age</th>
          </tr>
        </thead>
        <tbody>
          {items.map((it) => {
            const p = isPods ? podParts(it.status) : null
            const restarts = p ? Number(p.restarts) : 0
            return (
              <tr
                key={`${it.namespace}/${it.name}`}
                className="cursor-pointer border-b border-line/70 hover:bg-sunken/70"
                onClick={() => onOpen(it)}
              >
                <td className="max-w-[28rem] px-4 py-2.5">
                  <button
                    className="block max-w-full truncate text-left font-medium text-ink hover:text-blue"
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
                    <Pill tone={it.healthy ? 'ok' : 'bad'}>{p.state}</Pill>
                  ) : it.status ? (
                    <Pill tone={it.healthy ? 'plain' : 'bad'}>{it.status.split(' · ').join('  ')}</Pill>
                  ) : (
                    <span className="text-muted">—</span>
                  )}
                </td>
                {p && <td className="px-4 py-2.5 text-right tabular-nums">{p.ready}</td>}
                {p && (
                  <td className={`px-4 py-2.5 text-right tabular-nums ${restarts > 0 ? 'font-medium text-warn' : 'text-muted'}`}>
                    {p.restarts}
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
