import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import {
  Check,
  Download,
  Mic2,
  Play,
  Pause,
  Cpu,
  Sparkles,
  Trash2,
  ShieldCheck,
  PhoneOutgoing,
  RefreshCw,
  Search,
  Loader2,
} from "lucide-react";
import clsx from "clsx";
import {
  api,
  type DownloadJob,
  type GroqStatsResp,
  type LLMCatalogEntry,
  type LLMModel,
  type Voice,
  type VoiceCatalogEntry,
} from "../lib/api";
import { useEventStream } from "../lib/sse";

type Tab = "voice" | "llm" | "persona" | "echo" | "admin";

export default function SettingsPage() {
  const location = useLocation();
  /** Remount / refetch when navigating to Settings (fixes stale empty UI on first visit). */
  const navKey = location.key;
  const [tab, setTab] = useState<Tab>("voice");
  const [base, setBase] = useState<any>({});
  const [overrides, setOverrides] = useState<any>({});
  const [token, setToken] = useState(localStorage.getItem("admin_token") || "");
  const [saving, setSaving] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const [effectiveLLMProvider, setEffectiveLLMProvider] = useState<string>("local");

  const loadSettings = () =>
    api.settings().then((s) => {
      setBase(s.base);
      setOverrides(s.overrides || {});
      if (typeof s.effective_llm_provider === "string") {
        setEffectiveLLMProvider(s.effective_llm_provider);
      }
    });
  useEffect(() => { loadSettings(); }, [navKey]);

  const setOverride = (k: string, v: any) => {
    setOverrides((o: any) => ({ ...o, [k]: v }));
  };

  const save = async () => {
    setSaving(true);
    try {
      localStorage.setItem("admin_token", token);
      await api.patchSettings(overrides);
      await loadSettings();
      flashToast("Saved.");
    } catch (e) {
      flashToast(`Error: ${e}`);
    } finally {
      setSaving(false);
    }
  };

  const flashToast = (msg: string) => {
    setToast(msg);
    window.setTimeout(() => setToast(null), 2500);
  };

  const tabs: { id: Tab; label: string; icon: React.ReactNode }[] = [
    { id: "voice", label: "Voice & TTS", icon: <Mic2 size={16} /> },
    { id: "llm", label: "LLM", icon: <Cpu size={16} /> },
    { id: "persona", label: "Persona", icon: <Sparkles size={16} /> },
    { id: "echo", label: "Echo & VAD", icon: <PhoneOutgoing size={16} /> },
    { id: "admin", label: "Admin", icon: <ShieldCheck size={16} /> },
  ];

  return (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="px-10 pt-10 pb-4 flex items-start justify-between">
        <div>
          <div className="text-xs uppercase tracking-wider text-ink-400">Config</div>
          <h1 className="font-display text-4xl text-ink-900 mt-1">Settings</h1>
        </div>
        {toast && (
          <div className="px-3 py-1.5 rounded-full bg-ok/10 text-ok text-xs font-mono animate-fadeIn">
            {toast}
          </div>
        )}
      </div>

      <div className="px-10">
        <div className="inline-flex items-center gap-1 rounded-full border border-ink-100 bg-card p-1 shadow-soft">
          {tabs.map((t) => (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className={clsx(
                "px-4 py-1.5 rounded-full text-sm flex items-center gap-2 transition-colors",
                tab === t.id ? "bg-ink-800 text-paper" : "text-ink-600 hover:bg-ink-50",
              )}
            >
              {t.icon}
              {t.label}
            </button>
          ))}
        </div>
      </div>

      <div className="px-10 pb-16 pt-6 space-y-6">
        {tab === "voice" && <VoiceSection flash={flashToast} navKey={navKey} />}
        {tab === "llm" && (
          <LLMSection
            flash={flashToast}
            overrides={overrides}
            effectiveProvider={effectiveLLMProvider}
            reloadSettings={loadSettings}
            navKey={navKey}
          />
        )}
        {tab === "persona" && (
          <PersonaSection overrides={overrides} setOverride={setOverride} onSave={save} saving={saving} />
        )}
        {tab === "echo" && (
          <EchoSection base={base} overrides={overrides} setOverride={setOverride} onSave={save} saving={saving} />
        )}
        {tab === "admin" && (
          <AdminSection token={token} setToken={setToken} onSave={save} saving={saving} />
        )}
      </div>
    </div>
  );
}

// ---------- Voice ----------

function VoiceSection({ flash, navKey }: { flash: (s: string) => void; navKey: string }) {
  const [voices, setVoices] = useState<Voice[]>([]);
  const [catalog, setCatalog] = useState<VoiceCatalogEntry[]>([]);
  const [activeID, setActiveID] = useState<string>("");
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [installing, setInstalling] = useState<Set<string>>(new Set());

  const load = async () => {
    setLoading(true);
    try {
      const r = await api.listVoices();
      setVoices(r.voices || []);
      setCatalog(r.catalog || []);
      setActiveID(r.active_id || "");
      const installedNow = new Set((r.voices || []).map((v) => v.id));
      setInstalling((prev) => {
        const next = new Set<string>();
        prev.forEach((id) => { if (!installedNow.has(id)) next.add(id); });
        return next;
      });
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { load(); }, [navKey]);

  const markInstalling = (id: string) => {
    setInstalling((prev) => {
      const next = new Set(prev);
      next.add(id);
      return next;
    });
  };

  const installedIDs = new Set(voices.map((v) => v.id));
  const q = query.trim().toLowerCase();
  const filteredCatalog = catalog.filter((c) => {
    if (installedIDs.has(c.id)) return false;
    if (installing.has(c.id)) return false;
    if (!q) return true;
    return `${c.language} ${c.name} ${c.quality} ${c.accent}`.toLowerCase().includes(q);
  });

  const select = async (id: string) => {
    try {
      await api.selectVoice(id);
      setActiveID(id);
      flash(`Voice "${id}" is now active.`);
    } catch (e) {
      flash(`Select failed: ${e}`);
    }
  };

  return (
    <div className="space-y-6">
      <SectionCard
        icon={<Mic2 size={16} />}
        title="Installed voices"
        actions={
          <button
            onClick={load}
            className="text-xs text-ink-500 hover:text-ink-800 flex items-center gap-1"
          >
            <RefreshCw size={12} /> Rescan
          </button>
        }
      >
        {loading ? (
          <div className="text-sm text-ink-400">Loading…</div>
        ) : voices.length === 0 ? (
          <div className="text-sm text-ink-400">
            No voices installed yet. Pick one from the catalog below.
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
            {voices.map((v) => (
              <VoiceCard
                key={v.id}
                voice={v}
                active={v.id === activeID}
                onSelect={() => select(v.id)}
              />
            ))}
          </div>
        )}
      </SectionCard>

      <SectionCard
        icon={<Download size={16} />}
        title="Voice catalog"
        actions={
          <div className="relative">
            <Search size={14} className="absolute left-3 top-2 text-ink-300" />
            <input
              placeholder="Filter by language, accent…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="pl-8 pr-3 py-1.5 rounded-full border border-ink-100 bg-paper text-xs w-56"
            />
          </div>
        }
      >
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
          {filteredCatalog.map((entry) => (
            <CatalogVoiceCard
              key={entry.id}
              entry={entry}
              onInstalled={load}
              onStart={() => markInstalling(entry.id)}
              flash={flash}
            />
          ))}
          {filteredCatalog.length === 0 && (
            <div className="text-sm text-ink-400 col-span-3">No matches.</div>
          )}
        </div>
      </SectionCard>

      <DownloadsStrip kind="voice" onJobDone={load} />
    </div>
  );
}

function VoiceCard({ voice, active, onSelect }: { voice: Voice; active: boolean; onSelect: () => void }) {
  return (
    <div
      className={clsx(
        "rounded-2xl border p-4 transition-shadow",
        active ? "border-accent bg-accent/5 shadow-soft" : "border-ink-100 bg-paper hover:shadow-soft",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="font-display text-lg text-ink-800 truncate">{voice.name}</div>
          <div className="text-xs text-ink-500 font-mono mt-0.5">{voice.language} · {voice.quality}</div>
        </div>
        {active && (
          <span className="text-[10px] uppercase tracking-wider bg-accent text-white rounded-full px-2 py-0.5">
            Active
          </span>
        )}
      </div>
      <div className="text-xs text-ink-400 mt-2">
        {voice.sample_rate} Hz · {voice.size_mb.toFixed(1)} MB
      </div>
      <div className="mt-3 flex items-center gap-2">
        <SamplePlayer voiceID={voice.id} />
        <button
          onClick={onSelect}
          disabled={active}
          className={clsx(
            "text-xs font-medium rounded-full px-3 py-1.5 transition-colors",
            active ? "bg-ok/10 text-ok cursor-default" : "bg-ink-800 text-paper hover:bg-ink-700",
          )}
        >
          {active ? (
            <span className="flex items-center gap-1"><Check size={12} /> Selected</span>
          ) : (
            "Use voice"
          )}
        </button>
      </div>
    </div>
  );
}

function SamplePlayer({ voiceID }: { voiceID: string }) {
  const [state, setState] = useState<"idle" | "loading" | "playing">("idle");
  const audioRef = useRef<HTMLAudioElement | null>(null);

  useEffect(() => {
    return () => {
      audioRef.current?.pause();
      audioRef.current = null;
    };
  }, []);

  const toggle = async () => {
    if (state === "playing") {
      audioRef.current?.pause();
      setState("idle");
      return;
    }
    setState("loading");
    try {
      const audio = new Audio(api.voiceSampleURL(voiceID));
      audioRef.current = audio;
      audio.onended = () => setState("idle");
      audio.onerror = () => setState("idle");
      audio.oncanplay = () => setState("playing");
      await audio.play();
    } catch {
      setState("idle");
    }
  };

  return (
    <button
      onClick={toggle}
      className={clsx(
        "text-xs rounded-full px-3 py-1.5 border flex items-center gap-1",
        state === "playing"
          ? "border-accent bg-accent/10 text-accent"
          : "border-ink-100 text-ink-700 hover:bg-ink-50",
      )}
      title={state === "playing" ? "Pause preview" : "Play 6-second preview"}
    >
      {state === "loading" ? (
        <Loader2 size={12} className="animate-spin" />
      ) : state === "playing" ? (
        <Pause size={12} />
      ) : (
        <Play size={12} />
      )}
      Sample
    </button>
  );
}

function CatalogVoiceCard({
  entry,
  onInstalled,
  onStart,
  flash,
}: {
  entry: VoiceCatalogEntry;
  onInstalled: () => void;
  onStart: () => void;
  flash: (s: string) => void;
}) {
  const [pending, setPending] = useState(false);

  const install = async () => {
    if (pending) return;
    setPending(true);
    onStart();
    try {
      await api.startDownload({ kind: "voice", catalog_id: entry.id });
      flash(`Downloading "${entry.id}"…`);
      // On success: the parent's DownloadsStrip onJobDone -> load() will
      // refresh, the voice will appear in installed, and this card will
      // disappear from the catalog. On failure, free the button so the user
      // can retry.
      window.setTimeout(onInstalled, 1500);
    } catch (e) {
      flash(`Download failed: ${e}`);
      setPending(false);
    }
  };

  return (
    <div className="rounded-2xl border border-ink-100 bg-paper p-4 flex flex-col">
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="font-medium text-ink-800 truncate">{entry.id}</div>
          <div className="text-xs text-ink-500 mt-0.5">{entry.accent}</div>
        </div>
        <div className="text-[10px] font-mono text-ink-400 uppercase tracking-wider shrink-0">
          {entry.language} · {entry.quality}
        </div>
      </div>
      <div className="text-xs text-ink-400 mt-1">~{entry.approx_mb.toFixed(0)} MB</div>
      <button
        disabled={pending}
        onClick={install}
        className={clsx(
          "mt-3 inline-flex items-center justify-center gap-1 text-xs rounded-full px-3 py-1.5 transition-colors",
          pending
            ? "bg-ink-100 text-ink-400 cursor-wait"
            : "bg-ink-800 text-paper hover:bg-ink-700",
        )}
      >
        {pending ? <Loader2 size={12} className="animate-spin" /> : <Download size={12} />}
        {pending ? "Queued…" : "Install"}
      </button>
    </div>
  );
}

// ---------- LLM ----------

function barTone(pct: number, exhausted: boolean): string {
  if (exhausted) return "bg-ink-300";
  if (pct > 50) return "bg-ok";
  if (pct >= 20) return "bg-amber-400";
  return "bg-bad";
}

function formatCompact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return `${n}`;
}

function GroqRateDashboard() {
  const [data, setData] = useState<GroqStatsResp | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [fetchedAt, setFetchedAt] = useState<number | null>(null);
  const [now, setNow] = useState(Date.now());

  const load = async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await api.groqStats();
      setData(r);
      setFetchedAt(Date.now());
    } catch (e) {
      const msg = String(e);
      if (msg.includes("404") && msg.includes("/api/groq/stats")) {
        setErr(
          "deploy: The running agent binary is older than this UI — GET /api/groq/stats is not registered. Rebuild and restart the Go agent (e.g. go build -o /path/to/agent ./cmd/agent && sudo systemctl restart agent-voice), then refresh.",
        );
      } else {
        setErr(msg);
      }
      setData(null);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
    const id = window.setInterval(load, 30_000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- load once on mount
  }, []);

  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  const secAgo = fetchedAt != null ? Math.max(0, Math.floor((now - fetchedAt) / 1000)) : null;
  const totals = data?.totals;
  const stats = data?.stats ?? [];
  const model = data?.model ?? "llama-3.1-8b-instant";
  const rpdHeadroomPct =
    totals && totals.total_rpd_limit > 0
      ? (totals.total_remaining_rpd / totals.total_rpd_limit) * 100
      : 0;
  const estH = totals?.estimated_hours_left ?? 0;
  const estLabel =
    estH >= 1
      ? `${Math.floor(estH)}h ${Math.round((estH % 1) * 60)}m`
      : `${Math.round(estH * 60)} min`;

  return (
    <div className="rounded-2xl border border-ink-100 bg-ink-50/50 p-4 space-y-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="text-sm font-medium text-ink-800">Groq API Keys — {model}</div>
          <p className="text-[11px] text-ink-500 mt-1">
            Keys are configured only on the server (<code className="font-mono">GROQ_API_KEY</code> or{" "}
            <code className="font-mono">GROQ_API_KEYS</code>). Only masked suffixes are shown here.
          </p>
        </div>
        <button
          type="button"
          onClick={() => load()}
          disabled={loading}
          className="shrink-0 inline-flex items-center gap-1 rounded-xl border border-ink-200 bg-paper px-3 py-1.5 text-xs text-ink-700 hover:bg-ink-50 disabled:opacity-50"
        >
          {loading ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
          Refresh
        </button>
      </div>

      {err && !data && (
        <div
          className={clsx(
            "text-xs rounded-xl px-3 py-2 leading-relaxed",
            err.startsWith("deploy:")
              ? "border border-amber-200 bg-amber-50 text-amber-950"
              : "text-bad font-mono break-all",
          )}
        >
          {err.startsWith("deploy:") ? err.replace(/^deploy:\s*/, "") : err}
        </div>
      )}

      {loading && !data && !err && <div className="text-sm text-ink-400">Loading rate limits…</div>}

      {data && totals && (
        <>
          <div className="space-y-2">
            <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
              <span className="text-ink-700">
                Total remaining:{" "}
                <span className="font-mono font-medium">
                  {totals.total_remaining_rpd.toLocaleString()} / {totals.total_rpd_limit.toLocaleString()}
                </span>{" "}
                requests today
              </span>
            </div>
            <div className="text-sm text-ink-600">
              Est. time left (at 30 req/min): <span className="font-mono">{estLabel}</span>
            </div>
            <div className="h-2 w-full rounded-full bg-ink-100 overflow-hidden">
              <div
                className={clsx("h-full transition-all rounded-full", barTone(rpdHeadroomPct, false))}
                style={{ width: `${Math.min(100, Math.max(0, rpdHeadroomPct))}%` }}
              />
            </div>
            <div className="text-xs text-ink-500">
              Keys alive:{" "}
              <span className="font-mono text-ink-800">
                {totals.keys_alive}/{stats.length}
              </span>
            </div>
          </div>

          {data.cascade && (
            <div className="space-y-2 pt-2 border-t border-ink-100">
              <div className="text-sm font-medium text-ink-800">Cascade routing</div>
              {!data.cascade.env_flag && (
                <div className="rounded-xl border border-amber-200/80 bg-amber-50/80 px-3 py-2 text-[11px] text-amber-900">
                  <span className="font-medium">Off.</span> Set <code className="font-mono">LLM_CASCADE=1</code> in the
                  server environment and restart the agent to route simple turns to local{" "}
                  <code className="font-mono">{data.cascade.local_model}</code> and keep Groq for complex turns (
                  <code className="font-mono">{data.cascade.groq_model}</code>).
                </div>
              )}
              {data.cascade.env_flag && !data.cascade.routing_active && (
                <div className="rounded-xl border border-ink-200 bg-ink-50 px-3 py-2 text-[11px] text-ink-700">
                  <code className="font-mono">LLM_CASCADE</code> is set, but cascade needs the active LLM provider to be{" "}
                  <span className="font-mono">groq</span> (currently{" "}
                  <span className="font-mono">{data.cascade.llm_provider}</span>). Save LLM → Groq in Settings, or align
                  env so Groq is the effective provider.
                </div>
              )}
              {data.cascade.routing_active && data.router && (
                <>
                  <p className="text-[11px] text-ink-500">
                    Simple → local <code className="font-mono">{data.cascade.local_model}</code> · complex → Groq{" "}
                    <code className="font-mono">{data.cascade.groq_model}</code>. Counters reset when the agent process
                    restarts.
                  </p>
                  <div className="text-xs text-ink-600 space-y-1">
                    <div>
                      Local handled:{" "}
                      <span className="font-mono font-medium text-ink-800">
                        {data.router.local_requests.toLocaleString()}
                      </span>{" "}
                      ({data.router.local_pct.toFixed(0)}%) — 0 Groq tokens
                    </div>
                    <div>
                      Groq handled:{" "}
                      <span className="font-mono font-medium text-ink-800">
                        {data.router.groq_requests.toLocaleString()}
                      </span>{" "}
                      ({data.router.groq_pct.toFixed(0)}%) — {formatCompact(data.router.groq_tokens)} tokens
                    </div>
                    <div className="text-ink-500">
                      Quota offload: ~{data.router.quota_saved_pct.toFixed(0)}% of turns stayed local
                    </div>
                  </div>
                </>
              )}
            </div>
          )}

          <div className="space-y-3 pt-1 border-t border-ink-100">
            {stats.map((s) => (
              <div key={s.index} className="space-y-1.5">
                <div className="flex items-center justify-between text-xs">
                  <span className="text-ink-700 font-medium">
                    Key {s.index + 1}{" "}
                    <span className="font-mono text-ink-500">({s.key_suffix})</span>
                    {s.is_exhausted && (
                      <span className="ml-2 text-[10px] uppercase tracking-wider text-ink-400">exhausted</span>
                    )}
                  </span>
                </div>
                <div className="h-1.5 w-full rounded-full bg-ink-100 overflow-hidden">
                  <div
                    className={clsx("h-full transition-all", barTone(s.rpd_pct, s.is_exhausted))}
                    style={{ width: `${Math.min(100, Math.max(0, s.rpd_pct))}%` }}
                  />
                </div>
                <div className="text-[11px] text-ink-500 font-mono">
                  {s.remaining_rpd.toLocaleString()} RPD left · TPM {s.remaining_tpm.toLocaleString()}
                </div>
                <div className="h-1.5 w-full rounded-full bg-ink-100 overflow-hidden">
                  <div
                    className={clsx("h-full transition-all", barTone(s.tpd_pct, s.is_exhausted))}
                    style={{ width: `${Math.min(100, Math.max(0, s.tpd_pct))}%` }}
                  />
                </div>
                <div className="text-[11px] text-ink-500 font-mono">
                  {formatCompact(s.remaining_tpd)} TPD left
                </div>
              </div>
            ))}
          </div>

          <div className="text-[11px] text-ink-400 flex items-center justify-between pt-1 border-t border-ink-100">
            <span>
              Auto-refresh every 30s
              {secAgo != null && (
                <>
                  {" "}
                  · last updated: {secAgo}s ago
                </>
              )}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

function LLMSection({
  flash,
  overrides,
  effectiveProvider,
  reloadSettings,
  navKey,
}: {
  flash: (s: string) => void;
  overrides: Record<string, unknown>;
  effectiveProvider: string;
  reloadSettings: () => Promise<void>;
  navKey: string;
}) {
  const [provider, setProvider] = useState<"local" | "gemini" | "groq">("local");
  const [model, setModel] = useState("");
  const [geminiKeyDraft, setGeminiKeyDraft] = useState("");
  const [geminiFallback, setGeminiFallback] = useState("");
  const [remoteModels, setRemoteModels] = useState<string[]>([]);
  const [remoteLoading, setRemoteLoading] = useState(false);
  const [savingLLM, setSavingLLM] = useState(false);

  const [models, setModels] = useState<LLMModel[]>([]);
  const [catalog, setCatalog] = useState<LLMCatalogEntry[]>([]);
  const [activePath, setActivePath] = useState("");
  const [customURL, setCustomURL] = useState("");
  const [customName, setCustomName] = useState("");
  const [loading, setLoading] = useState(true);
  const [swapping, setSwapping] = useState<string | null>(null);
  const [installing, setInstalling] = useState<Set<string>>(new Set());

  useEffect(() => {
    const raw = String(overrides.llm_provider ?? "").trim().toLowerCase();
    if (raw === "gemini" || raw === "groq" || raw === "local") {
      setProvider(raw);
    } else {
      const e = effectiveProvider.toLowerCase();
      if (e === "gemini" || e === "groq" || e === "local") {
        setProvider(e);
      }
    }
    setModel(String(overrides.llm_model ?? ""));
    setGeminiFallback(String(overrides.gemini_model_fallback ?? ""));
    setGeminiKeyDraft("");
  }, [overrides, effectiveProvider]);

  const geminiMasked = overrides.gemini_api_key === "(configured)";

  const modelOptions = useMemo(() => {
    const s = new Set(remoteModels);
    const m = model.trim();
    if (m && !s.has(m)) {
      return [m, ...remoteModels];
    }
    return remoteModels;
  }, [remoteModels, model]);

  const refreshRemoteModels = async () => {
    if (provider !== "gemini" && provider !== "groq") return;
    setRemoteLoading(true);
    try {
      const ids = await api.listRemoteLLMModels(provider);
      setRemoteModels(ids);
      flash(`Loaded ${ids.length} model(s) from ${provider}.`);
    } catch (e) {
      flash(`Could not list models: ${e}`);
    } finally {
      setRemoteLoading(false);
    }
  };

  const saveRemoteSettings = async () => {
    if (provider !== "local" && !model.trim()) {
      flash("Choose a model (use Refresh list) or type one after loading.");
      return;
    }
    setSavingLLM(true);
    try {
      const patch: Record<string, unknown> = { llm_provider: provider };
      if (provider !== "local") {
        patch.llm_model = model.trim();
        if (provider === "gemini" && geminiFallback.trim()) {
          patch.gemini_model_fallback = geminiFallback.trim();
        }
        if (geminiKeyDraft.trim()) {
          patch.gemini_api_key = geminiKeyDraft.trim();
        }
      }
      await api.patchSettings(patch);
      await reloadSettings();
      flash("LLM settings saved.");
    } catch (e) {
      flash(`Save failed: ${e}`);
    } finally {
      setSavingLLM(false);
    }
  };

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.listModels();
      setModels(r.models || []);
      setCatalog(r.catalog || []);
      setActivePath(r.active_path || "");
      const installedNow = new Set((r.models || []).map((m) => m.id));
      setInstalling((prev) => {
        const next = new Set<string>();
        prev.forEach((id) => {
          if (!installedNow.has(id)) next.add(id);
        });
        return next;
      });
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    if (provider !== "local") return;
    void load();
  }, [navKey, provider, load]);

  const installedIDs = new Set(models.map((m) => m.id));
  const toInstall = catalog.filter((c) => !installedIDs.has(c.id) && !installing.has(c.id));

  const switchTo = async (id: string) => {
    setSwapping(id);
    try {
      const r = await api.selectModel(id);
      setActivePath(r.active_path);
      flash(`Switching LLM to "${id}"… llama-server restarting.`);
    } catch (e) {
      flash(`Switch failed: ${e}`);
    } finally {
      setSwapping(null);
    }
  };

  const installCatalog = async (id: string) => {
    if (installing.has(id)) return;
    setInstalling((prev) => {
      const next = new Set(prev);
      next.add(id);
      return next;
    });
    try {
      await api.startDownload({ kind: "llm", catalog_id: id });
      flash(`Downloading "${id}"…`);
      window.setTimeout(load, 2000);
    } catch (e) {
      flash(`Download failed: ${e}`);
      setInstalling((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  };

  const installCustom = async () => {
    if (!customURL || !customName) {
      flash("URL and filename are required.");
      return;
    }
    try {
      await api.startDownload({ kind: "llm", url: customURL, filename: customName });
      flash(`Downloading "${customName}"…`);
      setCustomURL("");
      setCustomName("");
    } catch (e) {
      flash(`Download failed: ${e}`);
    }
  };

  return (
    <div className="space-y-6">
      <SectionCard icon={<Cpu size={16} />} title="Inference provider">
        <p className="text-xs text-ink-500 -mt-1 mb-3">
          Use the local llama-server, or a hosted API (Gemini / Groq OpenAI-compatible). Keys are stored on the server;
          save requires an admin token (Admin tab).
        </p>
        <div className="flex flex-wrap gap-2 mb-4">
          {(["local", "gemini", "groq"] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setProvider(p)}
              className={clsx(
                "px-4 py-1.5 rounded-full text-xs font-medium transition-colors capitalize",
                provider === p ? "bg-ink-800 text-paper" : "bg-ink-50 text-ink-600 hover:bg-ink-100",
              )}
            >
              {p}
            </button>
          ))}
        </div>

        {provider !== "local" && (
          <div className="space-y-4 border border-ink-100 rounded-2xl p-4 bg-paper/80">
            {provider === "gemini" && (
              <Row label="Gemini API key">
                <div className="space-y-1.5">
                  <input
                    type="password"
                    autoComplete="off"
                    value={geminiKeyDraft}
                    onChange={(e) => setGeminiKeyDraft(e.target.value)}
                    placeholder={geminiMasked ? "Leave empty — key already saved" : "Paste API key"}
                    className="w-full rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm font-mono"
                  />
                  {geminiMasked && !geminiKeyDraft.trim() && (
                    <div className="text-xs text-ok flex items-center gap-1.5">
                      <Check size={12} className="shrink-0" />
                      Key is stored on the server; the box stays blank for security. Paste only to replace.
                    </div>
                  )}
                </div>
              </Row>
            )}
            {provider === "groq" && <GroqRateDashboard />}
            <Row label="Model">
              <div className="flex flex-col sm:flex-row gap-2">
                <input
                  list="remote-llm-model-ids"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder="Type a model id or pick after refresh"
                  className="flex-1 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm font-mono min-w-0"
                />
                <datalist id="remote-llm-model-ids">
                  {modelOptions.map((id) => (
                    <option key={id} value={id} />
                  ))}
                </datalist>
                <button
                  type="button"
                  onClick={refreshRemoteModels}
                  disabled={remoteLoading}
                  className="shrink-0 inline-flex items-center justify-center gap-1 rounded-xl border border-ink-200 bg-paper px-3 py-2 text-xs text-ink-700 hover:bg-ink-50 disabled:opacity-50"
                >
                  {remoteLoading ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}
                  Refresh list
                </button>
              </div>
            </Row>
            {provider === "gemini" && (
              <Row label="Fallback models (optional)">
                <input
                  value={geminiFallback}
                  onChange={(e) => setGeminiFallback(e.target.value)}
                  placeholder="comma-separated IDs if primary is unavailable"
                  className="w-full rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm font-mono"
                />
              </Row>
            )}
            <div className="flex justify-end pt-1">
              <button
                type="button"
                onClick={saveRemoteSettings}
                disabled={savingLLM}
                className="px-5 py-2 rounded-xl bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50 text-sm flex items-center gap-2"
              >
                {savingLLM ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
                {savingLLM ? "Saving…" : "Save LLM settings"}
              </button>
            </div>
          </div>
        )}

        {provider === "local" && (
          <div className="flex justify-end pt-1">
            <button
              type="button"
              onClick={saveRemoteSettings}
              disabled={savingLLM}
              className="px-5 py-2 rounded-xl bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50 text-sm flex items-center gap-2"
            >
              {savingLLM ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
              {savingLLM ? "Saving…" : "Save provider (local)"}
            </button>
          </div>
        )}
      </SectionCard>

      {provider === "local" && (
        <>
          <SectionCard
            icon={<Cpu size={16} />}
            title="Installed models"
            actions={
              <button onClick={() => void load()} className="text-xs text-ink-500 hover:text-ink-800 flex items-center gap-1">
                <RefreshCw size={12} /> Rescan
              </button>
            }
          >
            {loading ? (
              <div className="text-sm text-ink-400">Loading…</div>
            ) : models.length === 0 ? (
              <div className="text-sm text-ink-400">
                No local models. Install one from the catalog below — llama.cpp will reload
                automatically after download finishes.
              </div>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
                {models.map((m) => {
                  const active = m.file === activePath || m.active;
                  return (
                    <div
                      key={m.id}
                      className={clsx(
                        "rounded-2xl border p-4",
                        active ? "border-accent bg-accent/5 shadow-soft" : "border-ink-100 bg-paper",
                      )}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">
                          <div className="font-display text-lg text-ink-800 truncate" title={m.name}>
                            {m.name}
                          </div>
                          <div className="text-xs text-ink-500 font-mono mt-0.5">
                            {m.family} · {m.quant || "gguf"} · {m.size_gb.toFixed(2)} GB
                          </div>
                        </div>
                        {active && (
                          <span className="text-[10px] uppercase tracking-wider bg-accent text-white rounded-full px-2 py-0.5">
                            Active
                          </span>
                        )}
                      </div>
                      <button
                        onClick={() => switchTo(m.id)}
                        disabled={active || swapping === m.id}
                        className={clsx(
                          "mt-3 w-full text-xs rounded-full px-3 py-1.5",
                          active
                            ? "bg-ok/10 text-ok cursor-default"
                            : "bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50",
                        )}
                      >
                        {active ? (
                          <span className="flex items-center justify-center gap-1">
                            <Check size={12} /> In use
                          </span>
                        ) : swapping === m.id ? (
                          <span className="flex items-center justify-center gap-1">
                            <Loader2 size={12} className="animate-spin" /> Swapping…
                          </span>
                        ) : (
                          "Use model"
                        )}
                      </button>
                    </div>
                  );
                })}
              </div>
            )}
          </SectionCard>

          <SectionCard icon={<Download size={16} />} title="Model catalog">
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
              {toInstall.map((e) => (
                <div key={e.id} className="rounded-2xl border border-ink-100 bg-paper p-4 flex flex-col">
                  <div className="font-medium text-ink-800">{e.name}</div>
                  <div className="text-xs text-ink-500 font-mono mt-0.5">
                    {e.family} · {e.params} · {e.quant}
                  </div>
                  <div className="text-xs text-ink-400 mt-2 flex-1 leading-relaxed">{e.blurb}</div>
                  <div className="text-xs text-ink-400 mt-2">≈ {e.approx_gb.toFixed(2)} GB</div>
                  <button
                    onClick={() => installCatalog(e.id)}
                    className="mt-3 inline-flex items-center justify-center gap-1 text-xs rounded-full px-3 py-1.5 bg-ink-800 text-paper hover:bg-ink-700"
                  >
                    <Download size={12} /> Install
                  </button>
                </div>
              ))}
              {toInstall.length === 0 && (
                <div className="text-sm text-ink-400 col-span-3">All catalog models already installed.</div>
              )}
            </div>
          </SectionCard>

          <SectionCard icon={<Download size={16} />} title="Install custom GGUF">
            <p className="text-xs text-ink-500 mb-3">
              Paste any Hugging Face <code className="font-mono">/resolve/main/…gguf</code> URL and a filename.
              The file will land in <code className="font-mono">models/&lt;id&gt;/</code>.
            </p>
            <div className="grid grid-cols-1 md:grid-cols-5 gap-2">
              <input
                value={customURL}
                onChange={(e) => setCustomURL(e.target.value)}
                placeholder="https://huggingface.co/.../model.gguf"
                className="md:col-span-3 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm font-mono"
              />
              <input
                value={customName}
                onChange={(e) => setCustomName(e.target.value)}
                placeholder="model.gguf"
                className="md:col-span-1 rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm font-mono"
              />
              <button
                onClick={installCustom}
                className="md:col-span-1 rounded-xl bg-ink-800 text-paper text-sm px-3 py-2 hover:bg-ink-700"
              >
                Download
              </button>
            </div>
          </SectionCard>
        </>
      )}

      <DownloadsStrip kind="llm" onJobDone={load} />
    </div>
  );
}

// ---------- Downloads strip (live) ----------

function DownloadsStrip({
  kind,
  onJobDone,
}: {
  kind: "voice" | "llm";
  onJobDone?: () => void;
}) {
  const { events } = useEventStream<DownloadJob>("/api/downloads/stream");
  const [seeded, setSeeded] = useState<DownloadJob[]>([]);
  const seenDone = useRef<Set<string>>(new Set());

  useEffect(() => {
    api.listDownloads().then(setSeeded).catch(() => {});
  }, []);

  const merged = mergeDownloads(seeded, events).filter((j) => j.kind === kind);

  useEffect(() => {
    if (!onJobDone) return;
    let fired = false;
    for (const j of merged) {
      if (j.status === "done" && !seenDone.current.has(j.id)) {
        seenDone.current.add(j.id);
        fired = true;
      }
    }
    if (fired) {
      // Give the downloader a beat to finish the rename + fsync, then refresh.
      window.setTimeout(onJobDone, 400);
    }
  }, [merged, onJobDone]);

  if (merged.length === 0) return null;

  return (
    <SectionCard icon={<Download size={16} />} title="Downloads">
      <div className="space-y-2">
        {merged.map((j) => (
          <DownloadRow key={j.id} job={j} />
        ))}
      </div>
    </SectionCard>
  );
}

function DownloadRow({ job }: { job: DownloadJob }) {
  const pct =
    job.bytes_total > 0 ? Math.min(100, (job.bytes_done / job.bytes_total) * 100) : 0;
  const statusStyles: Record<string, string> = {
    done: "bg-ok/10 text-ok",
    running: "bg-accent/10 text-accent",
    queued: "bg-ink-100 text-ink-500",
    failed: "bg-bad/10 text-bad",
  };
  return (
    <div className="rounded-2xl border border-ink-100 bg-paper p-3">
      <div className="flex items-center justify-between">
        <div className="min-w-0">
          <div className="text-sm text-ink-800 truncate">{job.label}</div>
          <div className="text-[11px] text-ink-400 font-mono truncate">{job.url}</div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <span
            className={clsx(
              "text-[10px] uppercase tracking-wider rounded-full px-2 py-0.5",
              statusStyles[job.status] || "bg-ink-100 text-ink-500",
            )}
          >
            {job.status}
          </span>
          {job.status === "running" && (
            <button
              onClick={() => api.cancelDownload(job.id).catch(() => {})}
              className="text-ink-400 hover:text-bad"
              title="Cancel"
            >
              <Trash2 size={12} />
            </button>
          )}
        </div>
      </div>
      <div className="mt-2 h-1.5 w-full bg-ink-100 rounded-full overflow-hidden">
        <div
          className={clsx(
            "h-full transition-all",
            job.status === "failed" ? "bg-bad" : job.status === "done" ? "bg-ok" : "bg-accent",
          )}
          style={{ width: `${pct}%` }}
        />
      </div>
      <div className="flex items-center justify-between text-[11px] font-mono text-ink-500 mt-1">
        <span>
          {humanBytes(job.bytes_done)} / {job.bytes_total > 0 ? humanBytes(job.bytes_total) : "?"}
        </span>
        <span>
          {job.status === "running"
            ? `${job.speed_kbps.toFixed(0)} KB/s${job.eta_sec > 0 ? ` · ETA ${humanSec(job.eta_sec)}` : ""}`
            : job.error || ""}
        </span>
      </div>
    </div>
  );
}

function mergeDownloads(seeded: DownloadJob[], events: DownloadJob[]): DownloadJob[] {
  const map = new Map<string, DownloadJob>();
  for (const j of seeded) map.set(j.id, j);
  for (const j of events) map.set(j.id, j);
  return Array.from(map.values()).sort((a, b) => b.started_at - a.started_at);
}

function humanBytes(n: number): string {
  if (!n || n <= 0) return "0 B";
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(2)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)} KB`;
  return `${n} B`;
}

function humanSec(s: number): string {
  if (s >= 60) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${s}s`;
}

// ---------- Persona / Echo / Admin (reuse existing overrides) ----------

function PersonaSection({ overrides, setOverride, onSave, saving }: {
  overrides: any;
  setOverride: (k: string, v: any) => void;
  onSave: () => void;
  saving: boolean;
}) {
  return (
    <SectionCard icon={<Sparkles size={16} />} title="Persona (SDR prompt)">
      <Row label="System prompt">
        <textarea
          value={overrides.system_prompt ?? ""}
          onChange={(e) => setOverride("system_prompt", e.target.value)}
          placeholder={"Leave blank to use prompts/sdr.txt or the built-in default."}
          className="w-full rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm min-h-[160px]"
        />
      </Row>
      <Row label="Tone">
        <textarea
          value={overrides.system_tone ?? ""}
          onChange={(e) => setOverride("system_tone", e.target.value)}
          placeholder="Warm, professional, concise."
          className="w-full rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm min-h-[80px]"
        />
      </Row>
      <div className="flex justify-end pt-1">
        <SaveButton saving={saving} onSave={onSave} />
      </div>
    </SectionCard>
  );
}

function EchoSection({ base, overrides, setOverride, onSave, saving }: {
  base: any;
  overrides: any;
  setOverride: (k: string, v: any) => void;
  onSave: () => void;
  saving: boolean;
}) {
  return (
    <SectionCard icon={<PhoneOutgoing size={16} />} title="Echo & voice activity">
      <p className="text-xs text-ink-500 mb-3">
        Three layers: browser AEC3 (primary), server TTS-gate with reference correlation
        (secondary), semantic n-gram self-filter (tertiary).
      </p>
      <Row label="VAD threshold (0–1)">
        <input
          type="number" step="0.05" min="0" max="1"
          className="input"
          value={overrides.vad_threshold ?? base.VADThreshold ?? 0.55}
          onChange={(e) => setOverride("vad_threshold", Number(e.target.value))}
        />
      </Row>
      <Row label="TTS acoustic tail (ms)">
        <input
          type="number"
          className="input"
          value={overrides.tts_acoustic_tail_ms ?? base.TTSAcousticTailMs ?? 850}
          onChange={(e) => setOverride("tts_acoustic_tail_ms", Number(e.target.value))}
        />
      </Row>
      <Row label="Barge-in guard (ms)">
        <input
          type="number"
          className="input"
          value={overrides.barge_in_guard_ms ?? base.BargeInGuardMs ?? 550}
          onChange={(e) => setOverride("barge_in_guard_ms", Number(e.target.value))}
        />
      </Row>
      <div className="flex justify-end pt-1">
        <SaveButton saving={saving} onSave={onSave} />
      </div>
      <style>{`.input { width: 100%; border-radius: 12px; border: 1px solid #eeede9; background: #FAFAF7; padding: 8px 12px; font-size: 14px; outline: none; }`}</style>
    </SectionCard>
  );
}

function AdminSection({ token, setToken, onSave, saving }: {
  token: string;
  setToken: (s: string) => void;
  onSave: () => void;
  saving: boolean;
}) {
  return (
    <SectionCard icon={<ShieldCheck size={16} />} title="Admin">
      <Row label="Admin token">
        <input
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="paste AGENT_ADMIN_TOKEN from .env (leave blank if disabled)"
          className="w-full rounded-xl border border-ink-100 bg-paper px-3 py-2 text-sm font-mono"
        />
      </Row>
      <div className="text-xs text-ink-400 mt-1">
        Required to save settings, switch voices/models, and trigger downloads.
      </div>
      <div className="flex justify-end pt-1">
        <SaveButton saving={saving} onSave={onSave} />
      </div>
    </SectionCard>
  );
}

// ---------- primitives ----------

function SectionCard({ title, icon, actions, children }: {
  title: string;
  icon?: React.ReactNode;
  actions?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
      <div className="flex items-center justify-between mb-4">
        <div className="font-display text-xl flex items-center gap-2">
          {icon}
          {title}
        </div>
        {actions}
      </div>
      <div className="space-y-3">{children}</div>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-4 gap-3 items-center">
      <label className="text-xs text-ink-400 col-span-1">{label}</label>
      <div className="col-span-3">{children}</div>
    </div>
  );
}

function SaveButton({ saving, onSave }: { saving: boolean; onSave: () => void }) {
  return (
    <button
      onClick={onSave}
      disabled={saving}
      className="px-5 py-2 rounded-xl bg-ink-800 text-paper hover:bg-ink-700 disabled:opacity-50 text-sm flex items-center gap-2"
    >
      {saving ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
      {saving ? "Saving…" : "Save"}
    </button>
  );
}
