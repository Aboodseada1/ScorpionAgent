import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Phone, Building2 } from "lucide-react";
import { api, type Client } from "../lib/api";

export default function ClientsPage() {
  const [clients, setClients] = useState<Client[]>([]);
  const [showNew, setShowNew] = useState(false);
  const [q, setQ] = useState("");
  const nav = useNavigate();

  const refresh = () => api.listClients().then(setClients).catch(() => setClients([]));
  useEffect(() => {
    refresh();
  }, []);

  const filtered = q
    ? clients.filter((c) => (c.name + " " + c.business + " " + c.industry).toLowerCase().includes(q.toLowerCase()))
    : clients;

  return (
    <div className="h-full">
      <div className="px-10 pt-10 pb-6 flex items-end justify-between">
        <div>
          <div className="text-xs uppercase tracking-wider text-ink-400">Prospects</div>
          <h1 className="font-display text-4xl text-ink-900 mt-1">Clients</h1>
        </div>
        <div className="flex items-center gap-3">
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search"
            className="rounded-xl border border-ink-100 bg-card px-4 py-2 text-sm outline-none focus:ring-4 focus:ring-accent/10"
          />
          <button
            onClick={() => setShowNew(true)}
            className="inline-flex items-center gap-2 bg-ink-800 text-paper px-4 py-2 rounded-xl text-sm hover:bg-ink-700"
          >
            <Plus size={16} /> New client
          </button>
        </div>
      </div>

      <div className="px-10 pb-16 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
        {filtered.map((c) => (
          <div key={c.id} className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6 hover:border-accent/30 transition-colors">
            <div className="flex items-start gap-4">
              <div
                className="h-14 w-14 rounded-2xl grid place-items-center font-display text-xl text-ink-800"
                style={{ background: c.avatar_color || "#FFD6A5" }}
              >
                {initials(c.name)}
              </div>
              <div className="flex-1 min-w-0">
                <div className="font-display text-xl truncate">{c.name}</div>
                <div className="text-ink-500 text-sm flex items-center gap-2 mt-0.5 truncate">
                  <Building2 size={14} /> {c.business || "—"}
                </div>
              </div>
              <StageBadge stage={c.stage} />
            </div>

            <div className="mt-4 text-sm text-ink-500 line-clamp-2 min-h-[2.5rem]">{c.notes || "No notes yet."}</div>

            <div className="mt-5 flex items-center gap-3">
              <Link to={`/clients/${c.id}`} className="text-sm text-ink-700 hover:text-ink-900">
                Open profile →
              </Link>
              <button
                onClick={() => nav(`/clients/${c.id}/call`)}
                className="ml-auto inline-flex items-center gap-2 rounded-xl bg-ok text-white px-3.5 py-2 text-sm hover:opacity-90"
              >
                <Phone size={14} /> Call
              </button>
            </div>
          </div>
        ))}
        {filtered.length === 0 && (
          <div className="col-span-full rounded-3xl border border-dashed border-ink-200 py-16 grid-dots grid place-items-center text-ink-400">
            No clients yet. Add your first prospect to start calling.
          </div>
        )}
      </div>

      {showNew && <NewClientModal onClose={() => setShowNew(false)} onCreated={() => { refresh(); setShowNew(false); }} />}
    </div>
  );
}

function StageBadge({ stage }: { stage: string }) {
  const colors: Record<string, string> = {
    new: "bg-ink-50 text-ink-600",
    qualifying: "bg-accent/10 text-accent",
    meeting: "bg-ok/10 text-ok",
    lost: "bg-bad/10 text-bad",
    won: "bg-ok/10 text-ok",
  };
  return (
    <span className={`px-2.5 py-1 rounded-full text-xs ${colors[stage] || colors.new}`}>{stage || "new"}</span>
  );
}

function NewClientModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState({ name: "", business: "", industry: "", stage: "new", role: "seller", notes: "" });
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!form.name.trim()) return;
    setBusy(true);
    try {
      await api.createClient(form);
      onCreated();
    } catch (e) {
      alert(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 bg-ink-900/30 backdrop-blur-sm grid place-items-center z-40" onClick={onClose}>
      <div className="bg-paper rounded-3xl shadow-soft border border-ink-100 p-8 w-[520px] max-w-[95vw]" onClick={(e) => e.stopPropagation()}>
        <div className="font-display text-2xl mb-6">New client</div>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Name" v={form.name} on={(v) => setForm({ ...form, name: v })} />
          <Field label="Business" v={form.business} on={(v) => setForm({ ...form, business: v })} />
          <Field label="Industry" v={form.industry} on={(v) => setForm({ ...form, industry: v })} />
          <Select label="Role" v={form.role} on={(v) => setForm({ ...form, role: v })} options={["seller", "buyer", "financing", "other"]} />
          <Select label="Stage" v={form.stage} on={(v) => setForm({ ...form, stage: v })} options={["new", "qualifying", "meeting", "won", "lost"]} />
        </div>
        <div className="mt-4">
          <label className="text-xs text-ink-400">Notes</label>
          <textarea
            className="w-full mt-1 rounded-xl border border-ink-100 bg-card px-3 py-2 text-sm min-h-[100px] outline-none focus:ring-4 focus:ring-accent/10"
            value={form.notes}
            onChange={(e) => setForm({ ...form, notes: e.target.value })}
            placeholder="What should the AI know before calling them?"
          />
        </div>
        <div className="mt-6 flex items-center justify-end gap-3">
          <button onClick={onClose} className="px-4 py-2 rounded-xl text-ink-600 hover:bg-ink-50">Cancel</button>
          <button disabled={busy} onClick={submit} className="px-4 py-2 rounded-xl bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50">
            {busy ? "Saving…" : "Create"}
          </button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, v, on }: { label: string; v: string; on: (v: string) => void }) {
  return (
    <div>
      <label className="text-xs text-ink-400">{label}</label>
      <input
        value={v}
        onChange={(e) => on(e.target.value)}
        className="w-full mt-1 rounded-xl border border-ink-100 bg-card px-3 py-2 text-sm outline-none focus:ring-4 focus:ring-accent/10"
      />
    </div>
  );
}

function Select({ label, v, on, options }: { label: string; v: string; on: (v: string) => void; options: string[] }) {
  return (
    <div>
      <label className="text-xs text-ink-400">{label}</label>
      <select
        value={v}
        onChange={(e) => on(e.target.value)}
        className="w-full mt-1 rounded-xl border border-ink-100 bg-card px-3 py-2 text-sm outline-none focus:ring-4 focus:ring-accent/10"
      >
        {options.map((o) => (
          <option key={o} value={o}>{o}</option>
        ))}
      </select>
    </div>
  );
}

function initials(s: string) {
  const parts = s.trim().split(/\s+/);
  if (parts.length === 0) return "?";
  return (parts[0][0] + (parts[1]?.[0] || "")).toUpperCase();
}
