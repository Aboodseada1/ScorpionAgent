import { useEffect, useState } from "react";
import { api, type Client, type Graph } from "../lib/api";
import MemoryGraph from "../components/MemoryGraph";

export default function MemoryPage() {
  const [clients, setClients] = useState<Client[]>([]);
  const [filter, setFilter] = useState<string>("");
  const [graph, setGraph] = useState<Graph | null>(null);

  useEffect(() => {
    api.listClients().then(setClients).catch(() => setClients([]));
  }, []);

  useEffect(() => {
    api.memoryGraph(filter || undefined).then(setGraph).catch(() => setGraph({ nodes: [], edges: [] }));
  }, [filter]);

  return (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="px-10 pt-10 pb-6 flex items-end justify-between">
        <div>
          <div className="text-xs uppercase tracking-wider text-ink-400">Knowledge</div>
          <h1 className="font-display text-4xl text-ink-900 mt-1">Memory graph</h1>
        </div>
        <select
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="rounded-xl border border-ink-100 bg-card px-4 py-2 text-sm"
        >
          <option value="">All clients</option>
          {clients.map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </select>
      </div>

      <div className="px-10 pb-10">
        <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-2 h-[70vh]">
          {graph ? <MemoryGraph nodes={graph.nodes} edges={graph.edges} /> : <div className="p-8 text-ink-400">Loading…</div>}
        </div>
        <div className="mt-3 text-xs text-ink-400 flex items-center gap-4">
          <Legend color="#2F6FED" label="Client" />
          <Legend color="#1FA971" label="Conversation" />
          <Legend color="#E8A33C" label="Day" />
          <Legend color="#8b867a" label="Fact" />
          <Legend color="#D64545" label="Action" />
        </div>
      </div>
    </div>
  );
}

function Legend({ color, label }: { color: string; label: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="h-2.5 w-2.5 rounded-full" style={{ background: color }} />
      <span>{label}</span>
    </div>
  );
}
