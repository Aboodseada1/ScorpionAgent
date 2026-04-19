// Tiny typed fetch wrapper against the Go backend.

export interface Client {
  id: string;
  name: string;
  business: string;
  industry: string;
  stage: string;
  role: string;
  notes: string;
  avatar_color?: string;
  created_at: number;
  updated_at: number;
  profile?: Record<string, unknown>;
}

export interface Conversation {
  id: string;
  client_id?: string;
  started_at: number;
  ended_at?: number;
  summary: string;
  audio_dir: string;
}

export interface Turn {
  id: string;
  conversation_id: string;
  idx: number;
  speaker: string;
  text: string;
  t_start_ms: number;
  t_end_ms: number;
}

export interface Fact {
  id: string;
  client_id?: string;
  conversation_id?: string;
  day: string;
  subject: string;
  predicate: string;
  object: string;
  category: string;
  confidence: number;
  source_turn_id?: string;
  created_at: number;
}

export interface Action {
  id: string;
  client_id?: string;
  conversation_id?: string;
  type: string;
  payload: Record<string, unknown> | null;
  status: string;
  due_at?: number;
  created_at: number;
  executed_at?: number;
}

export interface Doc {
  id: string;
  client_id?: string;
  title: string;
  mime: string;
  body: string;
  created_at: number;
}

export interface Settings {
  base: Record<string, unknown>;
  overrides: Record<string, unknown>;
  effective_llm_provider?: string;
}

export interface StatusResp {
  ok: boolean;
  llm: { ok: boolean; base_url: string; model: string };
  /** Omitted when the app uses browser Web Speech instead of a server STT service. */
  stt?: { ok: boolean; base_url: string; model: string };
  piper: { ok: boolean; bin: string; model: string };
  server_time: number;
}

export interface Graph {
  nodes: { id: string; label: string; type: string; meta?: unknown }[];
  edges: { from: string; to: string; kind: string }[];
}

export interface Voice {
  id: string;
  name: string;
  language: string;
  quality: string;
  sample_rate: number;
  model_path: string;
  config_path: string;
  size_mb: number;
}

export interface VoiceCatalogEntry {
  id: string;
  name: string;
  language: string;
  quality: string;
  model_url: string;
  config_url: string;
  approx_mb: number;
  accent: string;
}

export interface VoicesResp {
  voices: Voice[];
  active_id: string;
  catalog: VoiceCatalogEntry[];
  install_dir: string;
}

export interface LLMModel {
  id: string;
  file: string;
  name: string;
  family: string;
  size_gb: number;
  quant: string;
  active: boolean;
}

export interface LLMCatalogEntry {
  id: string;
  name: string;
  family: string;
  params: string;
  quant: string;
  url: string;
  approx_gb: number;
  blurb: string;
}

export interface ModelsResp {
  models: LLMModel[];
  active_path: string;
  catalog: LLMCatalogEntry[];
  install_dir: string;
}

export interface DownloadJob {
  id: string;
  kind: "voice" | "llm";
  label: string;
  url: string;
  dest: string;
  bytes_done: number;
  bytes_total: number;
  speed_kbps: number;
  eta_sec: number;
  status: "queued" | "running" | "done" | "failed";
  error?: string;
  started_at: number;
  ended_at: number;
}

export interface GroqKeyStatRow {
  index: number;
  key_suffix: string;
  remaining_rpd: number;
  remaining_rpm: number;
  remaining_tpm: number;
  remaining_tpd: number;
  reset_requests?: string | null;
  reset_tokens?: string | null;
  is_exhausted: boolean;
  last_updated?: number | null;
  rpd_pct: number;
  tpd_pct: number;
  consecutive_429s: number;
}

export interface GroqTotalsRow {
  total_remaining_rpd: number;
  total_remaining_tpd: number;
  estimated_hours_left: number;
  keys_alive: number;
  total_rpd_limit: number;
  total_tpd_limit: number;
}

export interface RouterStatsRow {
  local_requests: number;
  groq_requests: number;
  groq_tokens: number;
  local_pct: number;
  groq_pct: number;
  quota_saved_pct: number;
  total_routed: number;
}

export interface GroqCascadeInfo {
  env_flag: boolean;
  routing_active: boolean;
  local_model: string;
  groq_model: string;
  llm_provider: string;
}

export interface GroqStatsResp {
  model: string;
  stats: GroqKeyStatRow[];
  totals: GroqTotalsRow;
  router?: RouterStatsRow;
  cascade?: GroqCascadeInfo;
}

export interface Metrics {
  ts: number;
  uptime_sec: number;
  hostname: string;
  kernel: string;
  cpu: {
    used_percent: number;
    cores: number;
    load1: number;
    load5: number;
    load15: number;
  };
  memory: {
    total_mb: number;
    used_mb: number;
    available_mb: number;
    used_percent: number;
    swap_total_mb: number;
    swap_used_mb: number;
  };
  disk: {
    mount: string;
    total_gb: number;
    used_gb: number;
    free_gb: number;
    used_percent: number;
  };
  net: {
    iface: string;
    rx_kbps: number;
    tx_kbps: number;
    rx_total_gb: number;
    tx_total_gb: number;
  };
  gpu?: {
    name: string;
    util_percent: number;
    mem_used_mb: number;
    mem_total_mb: number;
    temperature_c: number;
  };
  services: {
    name: string;
    active: string;
    sub_state: string;
    pid: number;
    memory_mb: number;
    cpu_nsec: number;
    cpu_percent: number;
    uptime_sec: number;
    description: string;
  }[];
}

function adminHeader(): HeadersInit {
  const t = localStorage.getItem("admin_token") || "";
  return t ? { Authorization: `Bearer ${t}`, "X-Admin-Token": t } : {};
}

async function jsonRequest<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...adminHeader(),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) {
    const t = await r.text().catch(() => "");
    throw new Error(`${method} ${path} ${r.status}: ${t}`);
  }
  if (r.status === 204) return undefined as T;
  return r.json();
}

export const api = {
  status: () => jsonRequest<StatusResp>("GET", "/api/status"),
  settings: () => jsonRequest<Settings>("GET", "/api/settings"),
  patchSettings: (delta: Record<string, unknown>) =>
    jsonRequest<{ ok?: boolean; overrides?: Record<string, unknown>; effective_llm_provider?: string }>(
      "PATCH",
      "/api/settings",
      delta,
    ),

  listClients: () => jsonRequest<{ clients: Client[] }>("GET", "/api/clients").then((r) => r.clients ?? []),
  createClient: (c: Partial<Client>) => jsonRequest<Client>("POST", "/api/clients", c),
  getClient: (id: string) => jsonRequest<Client>("GET", `/api/clients/${id}`),
  updateClient: (id: string, c: Partial<Client>) => jsonRequest<Client>("PUT", `/api/clients/${id}`, c),
  deleteClient: (id: string) => jsonRequest<{ ok: boolean }>("DELETE", `/api/clients/${id}`),
  clearClientData: (id: string, kind: "calls" | "memory" | "actions" | "docs" | "all") =>
    jsonRequest<{ ok: boolean; cleared: Record<string, number> }>(
      "POST",
      `/api/clients/${id}/clear?kind=${kind}`,
    ),

  listDocs: (clientID: string) => jsonRequest<{ docs: Doc[] }>("GET", `/api/clients/${clientID}/docs`).then((r) => r.docs ?? []),
  createDoc: (clientID: string, d: Partial<Doc>) => jsonRequest<Doc>("POST", `/api/clients/${clientID}/docs`, d),
  deleteDoc: (id: string) => jsonRequest<{ ok: boolean }>("DELETE", `/api/docs/${id}`),

  listConversations: (clientID: string) => jsonRequest<{ conversations: Conversation[] }>("GET", `/api/clients/${clientID}/conversations`).then((r) => r.conversations ?? []),
  getConversation: (id: string) => jsonRequest<Conversation>("GET", `/api/conversations/${id}`),
  listTurns: (convID: string) => jsonRequest<{ turns: Turn[] }>("GET", `/api/conversations/${convID}/turns`).then((r) => r.turns ?? []),

  listFacts: (clientID: string, convID?: string) => {
    const qs = convID ? `?conversation_id=${convID}` : "";
    return jsonRequest<{ facts: Fact[] }>("GET", `/api/clients/${clientID}/facts${qs}`).then((r) => r.facts ?? []);
  },

  listActions: (clientID: string, convID?: string) => {
    const qs = convID ? `?conversation_id=${convID}` : "";
    return jsonRequest<{ actions: Action[] }>("GET", `/api/clients/${clientID}/actions${qs}`).then((r) => r.actions ?? []);
  },

  memoryGraph: (clientID?: string) => {
    const qs = clientID ? `?client_id=${clientID}` : "";
    return jsonRequest<Graph>("GET", `/api/memory/graph${qs}`);
  },
  conversationGraph: (convID: string) => jsonRequest<Graph>("GET", `/api/conversations/${convID}/graph`),

  warmupSession: () =>
    jsonRequest<{
      ok: boolean;
      status: {
        llm?: { ok: boolean; ms: number; err?: string };
        tts?: { ok: boolean; ms: number; err?: string };
        stt?: { ok: boolean; ms: number; err?: string; url?: string };
      };
    }>("POST", "/api/session/warmup"),
  startSession: (clientID: string) =>
    jsonRequest<{ session_id: string; conversation_id: string }>("POST", "/api/session/start", { client_id: clientID }),
  endSession: (sessionID: string) => jsonRequest<{ ok: boolean }>("POST", `/api/session/${sessionID}/end`),
  textTurn: (sessionID: string, text: string) =>
    jsonRequest<{ ok: boolean }>("POST", `/api/session/${sessionID}/text`, { text }),

  // Voices
  listVoices: () => jsonRequest<VoicesResp>("GET", "/api/voices"),
  selectVoice: (id: string) => jsonRequest<{ ok: boolean }>("POST", `/api/voices/${id}/select`),
  voiceSampleURL: (id: string) => `/api/voices/${id}/sample`,

  // LLM models
  listModels: () => jsonRequest<ModelsResp>("GET", "/api/models"),
  selectModel: (id: string) => jsonRequest<{ ok: boolean; active_path: string }>("POST", `/api/models/${id}/select`),
  /** Lists models from Gemini or Groq (OpenAI-compatible GET /v1/models). Requires admin token when enabled. */
  listRemoteLLMModels: (provider: "gemini" | "groq") =>
    jsonRequest<{ models: string[] }>(
      "GET",
      `/api/models?remote_provider=${encodeURIComponent(provider)}`,
    ).then((r) => r.models ?? []),

  // Downloads
  listDownloads: () => jsonRequest<{ downloads: DownloadJob[] }>("GET", "/api/downloads").then((r) => r.downloads ?? []),
  startDownload: (body: { kind: "voice" | "llm"; catalog_id?: string; url?: string; filename?: string; voice_id?: string }) =>
    jsonRequest<DownloadJob>("POST", "/api/downloads", body),
  cancelDownload: (id: string) => jsonRequest<{ ok: boolean }>("POST", `/api/downloads/${id}/cancel`),

  // Metrics
  metrics: () => jsonRequest<Metrics>("GET", "/api/metrics"),

  /** Groq rate-limit dashboard (requires admin token). */
  groqStats: () => jsonRequest<GroqStatsResp>("GET", "/api/groq/stats"),
};
