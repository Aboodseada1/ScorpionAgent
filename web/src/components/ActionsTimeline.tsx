import type { Action } from "../lib/api";

export default function ActionsTimeline({ actions }: { actions: Action[] }) {
  if (actions.length === 0) {
    return <div className="rounded-3xl border border-dashed border-ink-200 py-16 grid-dots grid place-items-center text-ink-400">No actions yet.</div>;
  }
  return (
    <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-4">
      <ul className="divide-y divide-ink-50">
        {actions.map((a) => (
          <li key={a.id} className="py-4 px-2">
            <div className="flex items-start justify-between gap-4">
              <div>
                <div className="font-medium text-ink-800">{pretty(a.type)}</div>
                <pre className="text-xs text-ink-500 mt-1 whitespace-pre-wrap font-mono">{JSON.stringify(a.payload, null, 2)}</pre>
              </div>
              <div className="text-right text-xs">
                <div className="text-ink-400">{new Date(a.created_at * 1000).toLocaleString()}</div>
                <span className="inline-block mt-1 px-2 py-0.5 rounded-full bg-ink-50 text-ink-700">{a.status}</span>
              </div>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function pretty(t: string) { return t.replaceAll("_", " "); }
