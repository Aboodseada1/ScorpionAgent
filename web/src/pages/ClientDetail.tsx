import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Phone, FileText, Trash2, Upload, AlertTriangle } from "lucide-react";
import clsx from "clsx";
import { api, type Action, type Client, type Conversation, type Doc, type Fact } from "../lib/api";
import MemoryGraph from "../components/MemoryGraph";
import ActionsTimeline from "../components/ActionsTimeline";

type Tab = "overview" | "memory" | "calls" | "actions" | "docs";

export default function ClientDetailPage() {
  const { id = "" } = useParams<{ id: string }>();
  const nav = useNavigate();
  const [client, setClient] = useState<Client | null>(null);
  const [tab, setTab] = useState<Tab>("overview");
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [facts, setFacts] = useState<Fact[]>([]);
  const [actions, setActions] = useState<Action[]>([]);
  const [docs, setDocs] = useState<Doc[]>([]);

  const reload = async () => {
    try {
      const c = await api.getClient(id);
      setClient(c);
      const [conv, f, a, d] = await Promise.all([
        api.listConversations(id),
        api.listFacts(id),
        api.listActions(id),
        api.listDocs(id),
      ]);
      setConversations(conv);
      setFacts(f);
      setActions(a);
      setDocs(d);
    } catch {
      setClient(null);
    }
  };

  useEffect(() => {
    reload();
  }, [id]);

  const stats = useMemo(
    () => ({
      calls: conversations.length,
      facts: facts.length,
      actions: actions.length,
      docs: docs.length,
    }),
    [conversations, facts, actions, docs]
  );

  if (!client) return <div className="p-10 text-ink-400">Loading…</div>;

  return (
    <div className="h-full">
      <div className="px-10 pt-8 pb-4">
        <Link to="/clients" className="inline-flex items-center gap-2 text-sm text-ink-500 hover:text-ink-800">
          <ArrowLeft size={16} /> All clients
        </Link>
      </div>

      <div className="px-10 pb-6 flex items-start gap-6">
        <div
          className="h-24 w-24 rounded-3xl grid place-items-center font-display text-4xl text-ink-800 shadow-soft"
          style={{ background: client.avatar_color || "#FFD6A5" }}
        >
          {initials(client.name)}
        </div>
        <div className="flex-1 min-w-0">
          <h1 className="font-display text-4xl text-ink-900">{client.name}</h1>
          <div className="text-ink-500 mt-1">{client.business} {client.industry && <span className="text-ink-300 mx-1">·</span>} {client.industry}</div>
          <div className="flex items-center gap-4 text-xs text-ink-400 mt-2">
            <span>stage: <span className="text-ink-700">{client.stage}</span></span>
            <span>role: <span className="text-ink-700">{client.role}</span></span>
          </div>
        </div>
        <button
          onClick={() => nav(`/clients/${id}/call`)}
          className="inline-flex items-center gap-2 rounded-2xl bg-ok text-white px-5 py-3 text-sm font-medium shadow-soft hover:opacity-95"
        >
          <Phone size={16} /> Start call
        </button>
      </div>

      <div className="px-10 mb-6 flex items-center gap-2">
        {(["overview", "memory", "calls", "actions", "docs"] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={clsx(
              "px-4 py-2 rounded-xl text-sm capitalize",
              tab === t ? "bg-ink-800 text-paper" : "text-ink-600 hover:bg-ink-50"
            )}
          >
            {t}
            {t === "calls" && ` · ${stats.calls}`}
            {t === "memory" && ` · ${stats.facts}`}
            {t === "actions" && ` · ${stats.actions}`}
            {t === "docs" && ` · ${stats.docs}`}
          </button>
        ))}
      </div>

      <div className="px-10 pb-16">
        {tab === "overview" && <Overview client={client} onSaved={reload} facts={facts} actions={actions} stats={stats} />}
        {tab === "memory" && <MemoryTab clientID={id} facts={facts} />}
        {tab === "calls" && <CallsTab conversations={conversations} />}
        {tab === "actions" && <ActionsTimeline actions={actions} />}
        {tab === "docs" && <DocsTab clientID={id} docs={docs} onChange={reload} />}
      </div>
    </div>
  );
}

function Overview({ client, onSaved, facts, actions, stats }: {
  client: Client;
  onSaved: () => void;
  facts: Fact[];
  actions: Action[];
  stats: { calls: number; facts: number; actions: number; docs: number };
}) {
  const [form, setForm] = useState(client);
  const [saving, setSaving] = useState(false);

  useEffect(() => setForm(client), [client.id]);

  const save = async () => {
    setSaving(true);
    try {
      await api.updateClient(client.id, form);
      onSaved();
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
      <div className="lg:col-span-2 rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
        <div className="font-display text-xl mb-4">Profile</div>
        <div className="grid grid-cols-2 gap-4">
          <InputRow label="Name" v={form.name} on={(v) => setForm({ ...form, name: v })} />
          <InputRow label="Business" v={form.business} on={(v) => setForm({ ...form, business: v })} />
          <InputRow label="Industry" v={form.industry} on={(v) => setForm({ ...form, industry: v })} />
          <SelectRow label="Stage" v={form.stage} on={(v) => setForm({ ...form, stage: v })} opts={["new", "qualifying", "meeting", "won", "lost"]} />
          <SelectRow label="Role" v={form.role} on={(v) => setForm({ ...form, role: v })} opts={["seller", "buyer", "financing", "other"]} />
        </div>
        <div className="mt-4">
          <label className="text-xs text-ink-400">Notes</label>
          <textarea
            value={form.notes}
            onChange={(e) => setForm({ ...form, notes: e.target.value })}
            className="w-full mt-1 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm min-h-[140px] outline-none focus:ring-4 focus:ring-accent/10"
          />
        </div>
        <div className="mt-5 flex justify-end">
          <button onClick={save} disabled={saving} className="px-4 py-2 rounded-xl bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50">
            {saving ? "Saving…" : "Save"}
          </button>
        </div>
      </div>
      <div className="flex flex-col gap-6">
        <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
          <div className="font-display text-xl mb-4">Snapshot</div>
          <div className="space-y-3 text-sm text-ink-600">
            <div>{facts.length} learned facts</div>
            <div>{actions.length} actions logged</div>
          </div>
          <div className="mt-6 text-xs text-ink-400">
            The AI builds a memory graph from each call. Open the Memory tab to explore it.
          </div>
        </div>
        <DangerZone client={client} onDone={onSaved} stats={stats} />
      </div>
    </div>
  );
}

function DangerZone({
  client,
  onDone,
  stats,
}: {
  client: Client;
  onDone: () => void;
  stats: { calls: number; facts: number; actions: number; docs: number };
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const run = async (kind: "calls" | "memory" | "actions" | "docs" | "all", label: string, count: number) => {
    if (count === 0 && kind !== "all") {
      setMsg(`No ${label.toLowerCase()} to clear.`);
      window.setTimeout(() => setMsg(null), 2500);
      return;
    }
    const sure = window.confirm(
      kind === "all"
        ? `Wipe ALL data for ${client.name}? This removes calls, memory, actions and docs. Cannot be undone.`
        : `Clear ${label.toLowerCase()} for ${client.name}? This cannot be undone.`,
    );
    if (!sure) return;
    setBusy(kind);
    try {
      const r = await api.clearClientData(client.id, kind);
      const parts = Object.entries(r.cleared || {})
        .filter(([, n]) => n > 0)
        .map(([k, n]) => `${n} ${k}`)
        .join(" · ");
      setMsg(parts ? `Cleared ${parts}.` : `Already empty.`);
      window.setTimeout(() => setMsg(null), 3500);
      onDone();
    } catch (e) {
      setMsg(`Failed: ${e}`);
      window.setTimeout(() => setMsg(null), 4000);
    } finally {
      setBusy(null);
    }
  };

  const rows: { kind: "calls" | "memory" | "actions" | "docs"; label: string; count: number; hint: string }[] = [
    { kind: "calls",   label: "Calls",   count: stats.calls,   hint: "Conversations + transcripts" },
    { kind: "memory",  label: "Memory",  count: stats.facts,   hint: "Learned facts" },
    { kind: "actions", label: "Actions", count: stats.actions, hint: "Scheduled & executed actions" },
    { kind: "docs",    label: "Docs",    count: stats.docs,    hint: "Uploaded knowledge" },
  ];

  return (
    <div className="rounded-3xl bg-card border border-bad/20 shadow-soft p-6">
      <div className="flex items-center gap-2 font-display text-xl mb-1 text-bad">
        <AlertTriangle size={18} /> Danger zone
      </div>
      <div className="text-xs text-ink-400 mb-4">These operations cannot be undone.</div>
      <div className="divide-y divide-ink-50">
        {rows.map((r) => (
          <div key={r.kind} className="flex items-center justify-between py-3 first:pt-0 last:pb-0">
            <div className="min-w-0">
              <div className="text-sm text-ink-800 font-medium">{r.label} <span className="text-ink-400 font-normal">· {r.count}</span></div>
              <div className="text-xs text-ink-400 truncate">{r.hint}</div>
            </div>
            <button
              onClick={() => run(r.kind, r.label, r.count)}
              disabled={busy !== null}
              className="px-3 py-1.5 rounded-xl text-xs font-medium border border-bad/30 text-bad hover:bg-bad/10 disabled:opacity-40"
            >
              {busy === r.kind ? "Clearing…" : "Clear"}
            </button>
          </div>
        ))}
      </div>
      <button
        onClick={() => run("all", "Everything", stats.calls + stats.facts + stats.actions + stats.docs)}
        disabled={busy !== null}
        className="mt-4 w-full px-3 py-2 rounded-xl text-sm font-medium bg-bad text-white hover:opacity-95 disabled:opacity-40"
      >
        {busy === "all" ? "Wiping…" : "Wipe all client data"}
      </button>
      {msg && (
        <div className="mt-3 text-xs rounded-xl bg-paper border border-ink-100 px-3 py-2 text-ink-600">{msg}</div>
      )}
    </div>
  );
}

function MemoryTab({ clientID, facts }: { clientID: string; facts: Fact[] }) {
  const [graph, setGraph] = useState<{ nodes: any[]; edges: any[] } | null>(null);
  useEffect(() => {
    api.memoryGraph(clientID).then(setGraph).catch(() => setGraph({ nodes: [], edges: [] }));
  }, [clientID]);

  return (
    <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
      <div className="lg:col-span-2 rounded-3xl bg-card border border-ink-100 shadow-soft p-4 h-[520px]">
        {graph ? <MemoryGraph nodes={graph.nodes} edges={graph.edges} /> : <div className="text-ink-400 p-6">Loading graph…</div>}
      </div>
      <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
        <div className="font-display text-xl mb-4">Facts</div>
        <ul className="space-y-3 text-sm max-h-[480px] overflow-y-auto">
          {facts.map((f) => (
            <li key={f.id} className="border-l-2 border-accent pl-3">
              <div className="text-ink-800">{f.subject} <span className="text-ink-400">{f.predicate}</span> {f.object}</div>
              <div className="text-xs text-ink-400 mt-0.5">{f.category} · {new Date(f.created_at * 1000).toLocaleString()}</div>
            </li>
          ))}
          {facts.length === 0 && <li className="text-ink-400">No facts yet. Run a call.</li>}
        </ul>
      </div>
    </div>
  );
}

function CallsTab({ conversations }: { conversations: Conversation[] }) {
  if (conversations.length === 0)
    return <div className="rounded-3xl border border-dashed border-ink-200 py-16 grid-dots grid place-items-center text-ink-400">No calls yet.</div>;
  return (
    <div className="rounded-3xl bg-card border border-ink-100 shadow-soft divide-y divide-ink-50 overflow-hidden">
      {conversations.map((c) => (
        <Link
          key={c.id}
          to={`#`}
          className="block px-6 py-4 hover:bg-ink-50 transition-colors"
        >
          <div className="flex items-center justify-between">
            <div>
              <div className="text-ink-800 font-medium">{c.summary || "Untitled call"}</div>
              <div className="text-xs text-ink-400 mt-0.5">
                {new Date(c.started_at * 1000).toLocaleString()}{c.ended_at ? ` · ${formatDuration(c.started_at, c.ended_at)}` : " · ongoing"}
              </div>
            </div>
            <span className="text-xs text-ink-400 font-mono">{c.id.slice(0, 8)}</span>
          </div>
        </Link>
      ))}
    </div>
  );
}

function DocsTab({ clientID, docs, onChange }: { clientID: string; docs: Doc[]; onChange: () => void }) {
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);

  const add = async () => {
    if (!title.trim() || !body.trim()) return;
    setBusy(true);
    try {
      await api.createDoc(clientID, { title, body });
      setTitle("");
      setBody("");
      onChange();
    } finally {
      setBusy(false);
    }
  };

  const del = async (id: string) => {
    await api.deleteDoc(id);
    onChange();
  };

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
        <div className="font-display text-xl mb-4 flex items-center gap-2"><Upload size={18} /> Upload knowledge</div>
        <input
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Document title"
          className="w-full rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm outline-none focus:ring-4 focus:ring-accent/10"
        />
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="Paste or type context, FAQs, product details…"
          className="w-full mt-3 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm min-h-[220px] outline-none focus:ring-4 focus:ring-accent/10"
        />
        <div className="mt-4 flex justify-end">
          <button onClick={add} disabled={busy} className="px-4 py-2 rounded-xl bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50">
            {busy ? "Ingesting…" : "Add"}
          </button>
        </div>
      </div>
      <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
        <div className="font-display text-xl mb-4">Library</div>
        <ul className="space-y-2">
          {docs.map((d) => (
            <li key={d.id} className="flex items-start gap-3 rounded-xl hover:bg-ink-50 p-2">
              <FileText size={16} className="text-ink-400 mt-0.5" />
              <div className="flex-1 min-w-0">
                <div className="text-ink-800 font-medium truncate">{d.title}</div>
                <div className="text-xs text-ink-400 line-clamp-2">{d.body}</div>
              </div>
              <button className="text-ink-300 hover:text-bad" onClick={() => del(d.id)} title="Delete">
                <Trash2 size={16} />
              </button>
            </li>
          ))}
          {docs.length === 0 && <li className="text-ink-400 text-sm">No documents yet.</li>}
        </ul>
      </div>
    </div>
  );
}

function InputRow({ label, v, on }: { label: string; v: string; on: (v: string) => void }) {
  return (
    <div>
      <label className="text-xs text-ink-400">{label}</label>
      <input
        value={v}
        onChange={(e) => on(e.target.value)}
        className="w-full mt-1 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm outline-none focus:ring-4 focus:ring-accent/10"
      />
    </div>
  );
}

function SelectRow({ label, v, on, opts }: { label: string; v: string; on: (v: string) => void; opts: string[] }) {
  return (
    <div>
      <label className="text-xs text-ink-400">{label}</label>
      <select
        value={v}
        onChange={(e) => on(e.target.value)}
        className="w-full mt-1 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm outline-none focus:ring-4 focus:ring-accent/10"
      >
        {opts.map((o) => (
          <option key={o} value={o}>{o}</option>
        ))}
      </select>
    </div>
  );
}

function initials(s: string) {
  const parts = s.trim().split(/\s+/);
  return (parts[0]?.[0] || "?") + (parts[1]?.[0] || "");
}

function formatDuration(start: number, end: number) {
  const sec = Math.max(0, Math.round(end - start));
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}
