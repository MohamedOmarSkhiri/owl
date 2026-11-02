// Crash report under a CrashLoopBackOff or OOMKilled problem: how the container's last run
// ended, and the last lines it printed. The agent reads them as soon as the container dies,
// before Kubernetes throws that log away at the next restart.
import { Download } from 'lucide-react'
import { timeAgo, type Crash } from '../api'
import { saveFile, stamp } from '../download'

/** Exit code -> what it usually means, in a few words. */
export function exitMeaning(c: Pick<Crash, 'exitCode' | 'reason'>): string {
  if (c.reason === 'OOMKilled') return 'used more memory than its limit and was killed'
  switch (c.exitCode) {
    case 0:
      return 'exited normally, but is restarted (the app should keep running)'
    case 1:
      return 'the app stopped with an error'
    case 126:
      return 'the command cannot be executed (permissions)'
    case 127:
      return 'the command was not found in the image'
    case 137:
      return 'killed (SIGKILL): out of memory, or did not stop in time'
    case 139:
      return 'crashed on a memory access error (SIGSEGV)'
    case 143:
      return 'stopped (SIGTERM)'
  }
  return c.exitCode > 128 ? `killed by signal ${c.exitCode - 128}` : 'the app stopped with an error'
}

export function CrashReport({ crash, pod }: { crash: Crash; pod: string }) {
  const lines = crash.log ?? []
  const ended = timeAgo(crash.finishedAt)
  return (
    <div className="mt-2 rounded-lg border border-line bg-sunken/50 text-sm">
      <p className="px-3 py-2">
        <span className="font-medium">
          Exit code {crash.exitCode}
          {crash.reason && crash.reason !== 'Error' ? ` (${crash.reason})` : ''}
        </span>
        <span className="text-muted">
          : {exitMeaning(crash)}. Container {crash.container}, {crash.restarts} restart{crash.restarts === 1 ? '' : 's'}
          {ended && `, last ended ${ended} ago`}.
        </span>
      </p>
      {lines.length > 0 ? (
        <details className="group border-t border-line">
          <summary className="flex cursor-pointer items-center gap-3 px-3 py-2 text-blue select-none">
            <span className="shrink-0">Last {lines.length} lines before the crash</span>
            <code className="min-w-0 flex-1 truncate font-mono text-xs text-muted group-open:hidden">{lines[lines.length - 1]}</code>
          </summary>
          <div className="border-t border-line">
            <pre className="max-h-80 overflow-auto px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
              {lines.join('\n')}
            </pre>
            <div className="flex justify-end border-t border-line px-2 py-1.5">
              <button
                onClick={() => saveFile(`${pod}-${crash.container}-crash-${stamp()}.log`, lines.join('\n') + '\n')}
                className="flex items-center gap-1.5 rounded-md px-2.5 py-1 text-sm text-blue hover:bg-sunken"
              >
                <Download size={15} />
                Download
              </button>
            </div>
          </div>
        </details>
      ) : (
        <p className="border-t border-line px-3 py-2 text-muted">
          {crash.logError ? `No log from the last run: ${crash.logError}` : 'The last run printed nothing.'}
        </p>
      )}
    </div>
  )
}
