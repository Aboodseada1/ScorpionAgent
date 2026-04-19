import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Phone,
  PhoneOff,
  Mic,
  MicOff,
  Send,
  ArrowLeft,
  Sparkles,
  AlertTriangle,
  X,
  Check,
  Loader2,
} from "lucide-react";
import clsx from "clsx";
import { AnimatePresence, motion } from "framer-motion";
import { api, type Action, type Client } from "../lib/api";
import { sanitizeAssistantVisibleText } from "../lib/assistantSanitize";
import { CallTransport } from "../lib/rtc";

type TransportState = "idle" | "connecting" | "live" | "ended";
type AgentState = "idle" | "listening" | "thinking" | "speaking";
type PreloadState = "pending" | "running" | "ok" | "failed";
type WarmupKey = "llm" | "tts" | "stt";

interface ChatMessage {
  id: string;
  speaker: "user" | "assistant";
  /** Fully-received text. For an AI message this grows as llm_deltas stream in.
   *  For a user message this is set once the STT transcript lands. */
  fullText: string;
  /** How much of fullText is currently revealed by the typewriter animation. */
  revealed: number;
  t: number;
  /** Still awaiting content (no text yet). Used for the initial "..." ghost. */
  pending?: boolean;
  /** Content is still being produced (partial stream). Adds a live cursor. */
  partial?: boolean;
  /** User line: true while STT is still streaming (lighter caption); false when final. */
  sttInterim?: boolean;
  /** Cascade: which backend answered this assistant turn (server event). */
  llmRoute?: "local" | "groq";
}

interface Notice {
  id: number;
  stage: string;
  message: string;
  severity: "warn" | "error";
  t: number;
}

let noticeSeq = 0;
let msgSeq = 0;
const nextMsgID = () => `m${++msgSeq}`;

// Typewriter reveal speed: characters per animation frame (assistant final lines).
const REVEAL_CHARS_PER_FRAME = 2;

/** User live STT: fast tick + multi-word bursts when Whisper sends a big jump. */
const STT_WORD_MS = 10;

function revealNextWordChunk(fullText: string, revealed: number): number {
  if (revealed >= fullText.length) return revealed;
  const slice = fullText.slice(revealed);
  const lead = slice.match(/^(\s+)/);
  if (lead) return revealed + lead[1].length;
  const word = slice.match(/^(\S+)(\s*)/);
  if (word) return revealed + word[1].length + word[2].length;
  return revealed + 1;
}

/** How many words to reveal per tick — more when we're behind the server stream. */
function sttWordsPerTick(fullLen: number, revealed: number): number {
  const backlog = fullLen - revealed;
  if (backlog <= 0) return 0;
  if (backlog > 72) return 5;
  if (backlog > 40) return 3;
  if (backlog > 16) return 2;
  return 1;
}

export default function CallPage() {
  const { id = "" } = useParams<{ id: string }>();
  const [client, setClient] = useState<Client | null>(null);
  const [state, setState] = useState<TransportState>("idle");
  const [, setSessionID] = useState<string>("");
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [actions, setActions] = useState<Action[]>([]);
  const [notices, setNotices] = useState<Notice[]>([]);
  const [agentState, setAgentState] = useState<AgentState>("idle");
  /** Which backend is generating the reply (from llm_start); cleared when TTS starts. */
  const [pendingLLMRoute, setPendingLLMRoute] = useState<"local" | "groq" | null>(null);
  const [latency, setLatency] = useState<{ stt?: number; llm_first?: number; total?: number }>({});
  const llmStartAt = useRef<number | null>(null);
  const [muted, setMuted] = useState(false);
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const [typing, setTyping] = useState("");
  const [micLevel, setMicLevel] = useState(0);
  const [aiLevel, setAILevel] = useState(0);
  const [preload, setPreload] = useState<Record<WarmupKey, { status: PreloadState; ms?: number; err?: string }>>({
    llm: { status: "pending" },
    tts: { status: "pending" },
    stt: { status: "pending" },
  });
  const [preloading, setPreloading] = useState(false);
  const transport = useRef<CallTransport | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  /** While you are mid-utterance, show the big live caption; finalized lines stay in the thread. */
  const { threadMessages, liveUserCaption } = useMemo(() => {
    const last = messages[messages.length - 1];
    if (last?.speaker === "user" && last.sttInterim) {
      return { threadMessages: messages.slice(0, -1), liveUserCaption: last };
    }
    return { threadMessages: messages, liveUserCaption: null as ChatMessage | null };
  }, [messages]);

  useEffect(() => {
    api.getClient(id).then(setClient).catch(() => setClient(null));
  }, [id]);

  useEffect(() => {
    return () => {
      transport.current?.cleanup();
    };
  }, []);

  // User live STT: grow revealed word-by-word toward fullText (server sends longer strings over time).
  useEffect(() => {
    const userStreaming = messages.some(
      (m) =>
        m.speaker === "user" &&
        m.sttInterim === true &&
        m.revealed < m.fullText.length,
    );
    if (!userStreaming) return;
    const id = window.setInterval(() => {
      setMessages((ms) => {
        let changed = false;
        const next = ms.map((m) => {
          if (m.speaker !== "user" || m.sttInterim !== true || m.revealed >= m.fullText.length) {
            return m;
          }
          const nWords = sttWordsPerTick(m.fullText.length, m.revealed);
          let r = m.revealed;
          for (let w = 0; w < nWords && r < m.fullText.length; w++) {
            const nr = revealNextWordChunk(m.fullText, r);
            if (nr === r) break;
            r = nr;
          }
          if (r === m.revealed) return m;
          changed = true;
          return { ...m, revealed: r };
        });
        return changed ? next : ms;
      });
    }, STT_WORD_MS);
    return () => window.clearInterval(id);
  }, [messages]);

  // Assistant (and finalized user) lines: character typewriter toward fullText.
  useEffect(() => {
    const needsCharAnim = messages.some((m) => {
      if (m.revealed >= m.fullText.length) return false;
      // User interim lines use the word-by-word interval above.
      if (m.speaker === "user" && m.sttInterim === true) return false;
      return true;
    });
    if (!needsCharAnim) return;
    let raf = 0;
    const tick = () => {
      setMessages((ms) => {
        let changed = false;
        const next = ms.map((m) => {
          if (m.revealed >= m.fullText.length) return m;
          if (m.speaker === "user" && m.sttInterim === true) return m;
          changed = true;
          const step = m.partial ? REVEAL_CHARS_PER_FRAME : REVEAL_CHARS_PER_FRAME + 2;
          return { ...m, revealed: Math.min(m.fullText.length, m.revealed + step) };
        });
        return changed ? next : ms;
      });
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [messages]);

  // auto-scroll chat on any new content
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [messages, notices.length]);

  // Safety net: cull any pending ghost bubble that's been empty for too long.
  useEffect(() => {
    const hasStale = messages.some((m) => m.pending && !m.fullText);
    if (!hasStale) return;
    const handle = window.setTimeout(() => {
      setMessages((ms) => ms.filter((m) => !(m.pending && !m.fullText && Date.now() - m.t > 6000)));
    }, 6500);
    return () => window.clearTimeout(handle);
  }, [messages]);

  const dismissNotice = (noticeID: number) => {
    setNotices((n) => n.filter((x) => x.id !== noticeID));
  };

  const pushNotice = useCallback(
    (stage: string, message: string, severity: "warn" | "error" = "warn") => {
      setNotices((prev) => {
        const existing = prev.find((n) => n.stage === stage && n.message === message);
        if (existing) {
          return prev.map((n) => (n.id === existing.id ? { ...n, t: Date.now() } : n));
        }
        const nid = ++noticeSeq;
        window.setTimeout(() => dismissNotice(nid), 10000);
        return [...prev, { id: nid, stage, message, severity, t: Date.now() }];
      });
    },
    [],
  );

  const start = async () => {
    if (!client) return;
    setMessages([]);
    setActions([]);
    setNotices([]);
    setLatency({});
    setAgentState("idle");
    setPendingLLMRoute(null);
    llmStartAt.current = null;

    // ---- Phase 1: warmup every backing service in parallel. This makes the
    // first turn feel instant (LLM kv-cache hot, piper paged into RAM).
    setPreloading(true);
    setPreload({ llm: { status: "running" }, tts: { status: "running" }, stt: { status: "running" } });
    try {
      const res = await api.warmupSession();
      const next: typeof preload = { llm: { status: "pending" }, tts: { status: "pending" }, stt: { status: "pending" } };
      (Object.keys(res.status) as WarmupKey[]).forEach((k) => {
        const s = res.status[k];
        next[k] = { status: s.ok ? "ok" : "failed", ms: s.ms, err: s.err };
      });
      setPreload(next);
      if (!res.ok) {
        pushNotice(
          "warmup",
          Object.entries(res.status)
            .filter(([, v]) => !v.ok)
            .map(([k, v]) => `${k}: ${v.err || "unreachable"}`)
            .join(" · "),
          "error",
        );
        setPreloading(false);
        return;
      }
    } catch (err) {
      pushNotice("warmup", String(err), "error");
      setPreloading(false);
      return;
    }

    // ---- Phase 2: start the session + open the WS. Every service is now hot.
    try {
      const { session_id } = await api.startSession(client.id);
      setSessionID(session_id);
      const t = new CallTransport();
      transport.current = t;
      t.onStateChange = (s) => {
        setState(s);
        if (s === "live") setStartedAt(Date.now());
        if (s === "ended") setPreloading(false);
      };
      t.onEvent = (e) => onEvent(e);
      t.onMicLevel = (l) => setMicLevel(l);
      t.onAILevel = (l) => setAILevel(l);
      await t.start(session_id);
      setPreloading(false);
    } catch (err) {
      pushNotice("startup", String(err), "error");
      setState("ended");
      setPreloading(false);
    }
  };

  const hangup = async () => {
    transport.current?.hangup();
    setPendingLLMRoute(null);
    setState("ended");
  };

  const onEvent = (e: { kind: string; payload?: any }) => {
    switch (e.kind) {
      case "state": {
        const s = (e.payload?.state || "idle") as AgentState;
        setAgentState(s);
        if (s === "listening" || s === "idle") {
          setMessages((m) => dropPending(m));
          setPendingLLMRoute(null);
        }
        break;
      }
      case "voice_start": {
        setMessages((m) => {
          const last = m[m.length - 1];
          if (last && last.speaker === "user" && last.pending) return m;
          return [
            ...m,
            {
              id: nextMsgID(),
              speaker: "user",
              fullText: "",
              revealed: 0,
              t: Date.now(),
              pending: true,
              partial: true,
            },
          ];
        });
        break;
      }
      case "utterance_empty":
      case "self_filtered": {
        setMessages((m) => dropPendingUser(m));
        break;
      }
      case "transcript_partial": {
        const text = String(e.payload?.text || "");
        if (!text) break;
        setMessages((m) => updateUserPartialCaption(m, text));
        break;
      }
      case "transcript": {
        const speaker = (e.payload?.speaker || "user") as "user" | "assistant";
        const text = String(e.payload?.text || "");
        const route = e.payload?.route as "local" | "groq" | undefined;
        setMessages((m) => finalizeMessage(m, speaker, text, route));
        if (speaker === "user" && e.payload?.latency_ms != null) {
          setLatency((l) => ({ ...l, stt: Number(e.payload.latency_ms) }));
        }
        if (speaker === "assistant") {
          setLatency((l) => {
            const st = l.stt ?? 0;
            const ft = l.llm_first ?? 0;
            if (st > 0 && ft > 0) return { ...l, total: Math.round(st + ft) };
            return l;
          });
        }
        break;
      }
      case "llm_start": {
        llmStartAt.current = Date.now();
        const route = e.payload?.route as "local" | "groq" | undefined;
        setPendingLLMRoute(route ?? null);
        setAgentState("thinking");
        setMessages((m) => {
          const last = m[m.length - 1];
          if (last && last.speaker === "assistant" && last.partial) return m;
          return [
            ...m,
            {
              id: nextMsgID(),
              speaker: "assistant",
              fullText: "",
              revealed: 0,
              t: Date.now(),
              pending: true,
              partial: true,
              llmRoute: route,
            },
          ];
        });
        break;
      }
      case "llm_delta": {
        const d = String(e.payload?.delta || "");
        if (!d) break;
        if (llmStartAt.current != null) {
          const ms = Date.now() - llmStartAt.current;
          setLatency((l) => (l.llm_first != null ? l : { ...l, llm_first: ms }));
          llmStartAt.current = null;
        }
        setMessages((m) => appendAssistantDelta(m, d));
        break;
      }
      case "barge_in":
        transport.current?.muteIncomingTTS();
        break;
      case "tts_start":
        transport.current?.unmuteIncomingTTS();
        setPendingLLMRoute(null);
        setAgentState("speaking");
        break;
      case "tts_end":
        setAgentState("listening");
        break;
      case "stt_start":
        // Stay on listening — "thinking" is only while the LLM runs (after STT).
        break;
      case "action":
        setActions((a) => [e.payload as Action, ...a]);
        break;
      case "usage":
        setLatency((l) => ({ ...l, total: Date.now() - (startedAt || Date.now()) }));
        break;
      case "error":
        pushNotice(e.payload?.stage || "runtime", e.payload?.err || "unknown error", "error");
        break;
      case "notice":
        pushNotice(e.payload?.stage || "info", e.payload?.message || "", "warn");
        break;
    }
  };

  const sendText = () => {
    if (!transport.current || !typing.trim()) return;
    const text = typing.trim();
    transport.current.sendText(text);
    setMessages((m) => [
      ...m,
      { id: nextMsgID(), speaker: "user", fullText: text, revealed: text.length, t: Date.now() },
    ]);
    setTyping("");
  };

  const ticking = useClock();
  const elapsed = startedAt ? msToClock(ticking - startedAt) : "0:00";

  if (!client) {
    return (
      <div className="min-h-full grid place-items-center bg-paper text-ink-400">
        <p className="text-sm">Loading…</p>
      </div>
    );
  }

  const inPreload = preloading && state !== "live";
  const isLive = state === "live";

  return (
    <motion.div
      className="h-full min-h-0 flex flex-col bg-gradient-to-br from-paper via-paper to-[#FFF6EE] text-ink-800 font-sans"
      initial={{ opacity: 0.92 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.35 }}
    >
      <header className="shrink-0 flex items-center justify-between gap-3 px-4 py-3 md:px-8 border-b border-ink-100/80 bg-card/85 backdrop-blur-md">
        <Link
          to={`/clients/${client.id}`}
          className="inline-flex items-center gap-2 text-sm text-ink-500 hover:text-ink-900 transition"
        >
          <ArrowLeft size={16} /> Back to profile
        </Link>
        <div className="flex items-center gap-3 min-w-0 justify-center flex-1">
          <div
            className="h-10 w-10 rounded-full grid place-items-center font-display text-sm text-ink-800 shadow-soft shrink-0 ring-2 ring-white/80"
            style={{ background: client.avatar_color || "#FFD6A5" }}
          >
            {initials(client.name)}
          </div>
          <div className="min-w-0 text-left hidden sm:block">
            <div className="font-display text-sm text-ink-900 leading-tight truncate">{client.name}</div>
            <div className="text-[11px] text-ink-400 truncate">{client.business || "—"}</div>
          </div>
          <StatusPill state={state} agent={agentState} />
        </div>
        <div className="text-xs text-ink-400 font-mono tabular-nums min-w-[4rem] text-right">
          {isLive ? `live · ${elapsed}` : state}
        </div>
      </header>

      {inPreload ? (
        <PreloadScreen preload={preload} />
      ) : (
        <div className="flex-1 min-h-0 flex flex-col lg:flex-row overflow-hidden">
          <div className="flex-1 min-h-0 flex flex-col min-w-0 lg:border-r border-ink-100/80">
            {isLive && liveUserCaption && (
              <LiveUserHero m={liveUserCaption} micLevel={micLevel} muted={muted} />
            )}

            <div
              ref={scrollRef}
              className="flex-1 min-h-0 overflow-y-auto px-4 py-5 md:px-8 md:py-8 grid-dots"
            >
              {threadMessages.length === 0 && !liveUserCaption ? (
                <CallLobby state={state} />
              ) : (
                <ul className="space-y-4 max-w-2xl mx-auto w-full">
                  <AnimatePresence initial={false}>
                    {threadMessages.map((m) => (
                      <MessageBubble key={m.id} m={m} />
                    ))}
                  </AnimatePresence>
                  {threadMessages.length === 0 && liveUserCaption && (
                    <li className="text-center text-sm text-ink-400 py-8">Assistant reply will appear here.</li>
                  )}
                </ul>
              )}
            </div>

            <div className="shrink-0 flex items-center gap-3 px-4 py-3 md:px-6 border-t border-ink-100 bg-card/90">
              {state !== "live" ? (
                <button
                  type="button"
                  onClick={start}
                  disabled={state === "connecting" || preloading}
                  className="inline-flex items-center justify-center gap-2 rounded-full bg-gradient-to-br from-ok to-emerald-600 text-white px-6 py-2.5 text-sm font-medium shadow-soft hover:opacity-95 disabled:opacity-50 transition"
                  title="Start call"
                >
                  {preloading ? (
                    <>
                      <Loader2 size={16} className="animate-spin" /> Warming up…
                    </>
                  ) : (
                    <>
                      <Phone size={16} />
                      {state === "connecting" ? "Connecting…" : "Start call"}
                    </>
                  )}
                </button>
              ) : (
                <>
                  <button
                    type="button"
                    onClick={() => {
                      const next = !muted;
                      setMuted(next);
                      transport.current?.setMuted(next);
                    }}
                    className={clsx(
                      "h-11 w-11 rounded-full grid place-items-center shrink-0 transition shadow-soft border border-ink-100",
                      muted ? "bg-ink-800 text-paper" : "bg-paper text-ink-700 hover:border-ink-300",
                    )}
                    title={muted ? "Unmute" : "Mute"}
                  >
                    {muted ? <MicOff size={17} /> : <Mic size={17} />}
                  </button>
                  <button
                    type="button"
                    onClick={hangup}
                    className="h-11 w-11 rounded-full bg-bad text-white grid place-items-center shrink-0 hover:opacity-95 transition shadow-soft"
                    title="Hang up"
                  >
                    <PhoneOff size={17} />
                  </button>
                </>
              )}
              <input
                value={typing}
                onChange={(e) => setTyping(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && sendText()}
                placeholder={state === "live" ? "Or type as the prospect…" : "Start the call, then speak or type"}
                disabled={state !== "live"}
                className="flex-1 rounded-full border border-ink-100 bg-paper px-4 py-2.5 text-sm text-ink-900 outline-none focus:ring-4 focus:ring-accent/15 disabled:opacity-50"
              />
              <button
                type="button"
                onClick={sendText}
                disabled={state !== "live" || !typing.trim()}
                className="p-2.5 rounded-full bg-ink-800 text-paper disabled:opacity-35 shrink-0 transition hover:bg-ink-900"
                title="Send"
              >
                <Send size={15} />
              </button>
            </div>
          </div>

          <aside className="lg:w-[min(380px,100%)] shrink-0 flex flex-col border-t lg:border-t-0 lg:border-l border-ink-100/80 bg-card/50 min-h-0 overflow-y-auto">
            {isLive && (
              <VoicePanel
                client={client}
                agentState={agentState}
                micLevel={micLevel}
                aiLevel={aiLevel}
                muted={muted}
                pendingLLMRoute={pendingLLMRoute}
              />
            )}

            <div className="p-5 md:p-6 space-y-5">
              <div>
                <div className="flex items-center gap-2 text-sm font-display text-ink-600 mb-2">
                  <Sparkles size={16} className="text-accent" /> Live actions
                </div>
                {actions.length === 0 ? (
                  <p className="text-ink-500 text-sm leading-relaxed">
                    Scheduling, qualification, and notes show up here as the agent works.
                  </p>
                ) : (
                  <ul className="space-y-2">
                    {actions.map((a) => (
                      <li key={a.id} className="rounded-2xl bg-paper border border-ink-100 p-3">
                        <div className="text-sm font-medium text-ink-800">{prettyAction(a.type)}</div>
                        <pre className="text-xs text-ink-500 mt-1 whitespace-pre-wrap font-mono leading-snug">
                          {JSON.stringify(a.payload, null, 2)}
                        </pre>
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div className="rounded-2xl border border-ink-100 bg-paper/80 p-4 text-xs text-ink-600">
                <p className="mb-3 leading-relaxed">
                  With cascade on, some short replies use the local model; most use Groq. AI bubbles show Local or Groq.
                </p>
                <div className="font-mono space-y-1 text-ink-800">
                  <div className="flex justify-between gap-4">
                    <span className="text-ink-500">STT</span>
                    <span className="tabular-nums">{latency.stt != null ? `${latency.stt} ms` : "—"}</span>
                  </div>
                  <div className="flex justify-between gap-4">
                    <span className="text-ink-500">LLM first</span>
                    <span className="tabular-nums">{latency.llm_first != null ? `${latency.llm_first} ms` : "—"}</span>
                  </div>
                  <div className="flex justify-between gap-4">
                    <span className="text-ink-500">Turn total</span>
                    <span className="tabular-nums">{latency.total != null ? `${latency.total} ms` : "—"}</span>
                  </div>
                </div>
              </div>

              {notices.length > 0 && (
                <div>
                  <div className="flex items-center gap-2 text-sm font-display text-ink-600 mb-2">
                    <AlertTriangle size={16} className="text-warn" /> Notices
                  </div>
                  <ul className="space-y-2">
                    {notices.map((n) => (
                      <li
                        key={n.id}
                        className={clsx(
                          "flex items-start gap-2 rounded-2xl border px-3 py-2 text-xs",
                          n.severity === "error"
                            ? "bg-bad/5 border-bad/25 text-bad"
                            : "bg-warn/5 border-warn/25 text-ink-700",
                        )}
                      >
                        <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                        <div className="flex-1 leading-snug break-words">
                          <span className="font-mono uppercase text-[10px] opacity-70 mr-1">{n.stage}</span>
                          {n.message}
                        </div>
                        <button
                          type="button"
                          onClick={() => dismissNotice(n.id)}
                          className="opacity-50 hover:opacity-100"
                          title="Dismiss"
                        >
                          <X size={12} />
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          </aside>
        </div>
      )}
    </motion.div>
  );
}

function PreloadScreen({ preload }: { preload: Record<WarmupKey, { status: PreloadState; ms?: number; err?: string }> }) {
  const steps: { key: WarmupKey; label: string; hint: string }[] = [
    { key: "llm", label: "Language model", hint: "KV cache warm" },
    { key: "tts", label: "Voice engine", hint: "Piper" },
    { key: "stt", label: "Transcriber", hint: "Whisper" },
  ];
  return (
    <motion.div
      className="flex-1 grid place-items-center px-6 bg-gradient-to-br from-paper to-[#FFF6EE]"
      initial={{ opacity: 0.9 }}
      animate={{ opacity: 1 }}
    >
      <div className="max-w-lg w-full text-center">
        <motion.div
          className="inline-flex h-16 w-16 rounded-full bg-gradient-to-br from-accent to-ok text-white place-items-center mb-6 shadow-soft"
          animate={{ scale: [1, 1.04, 1] }}
          transition={{ duration: 2, repeat: Infinity, ease: "easeInOut" }}
        >
          <Phone size={26} />
        </motion.div>
        <div className="font-display text-2xl md:text-3xl text-ink-800 mb-2">Warming up the line</div>
        <p className="text-ink-500 text-sm mb-8">LLM, voice, and speech services — then you are live.</p>
        <ul className="space-y-2 text-left">
          {steps.map((s, i) => {
            const v = preload[s.key];
            return (
              <motion.li
                key={s.key}
                initial={{ opacity: 0, x: -8 }}
                animate={{ opacity: 1, x: 0 }}
                transition={{ delay: 0.05 * i }}
                className={clsx(
                  "flex items-center gap-3 rounded-2xl border px-4 py-3 bg-card/80",
                  v.status === "ok" ? "border-ok/30" : v.status === "failed" ? "border-bad/30" : "border-ink-100",
                )}
              >
                <div
                  className={clsx(
                    "h-8 w-8 rounded-full grid place-items-center shrink-0 text-xs",
                    v.status === "ok"
                      ? "bg-ok/10 text-ok"
                      : v.status === "failed"
                        ? "bg-bad/10 text-bad"
                        : "bg-ink-100 text-ink-500",
                  )}
                >
                  {v.status === "ok" ? <Check size={14} /> : v.status === "failed" ? <X size={14} /> : <Loader2 size={14} className="animate-spin" />}
                </div>
                <div className="flex-1 min-w-0">
                  <div className="font-display text-sm text-ink-800">{s.label}</div>
                  <div className="text-[11px] text-ink-400 truncate">{v.err ? v.err : v.ms != null ? `${v.ms} ms` : s.hint}</div>
                </div>
              </motion.li>
            );
          })}
        </ul>
      </div>
    </motion.div>
  );
}

function VoicePanel({
  client,
  agentState,
  micLevel,
  aiLevel,
  muted,
  pendingLLMRoute,
}: {
  client: Client;
  agentState: AgentState;
  micLevel: number;
  aiLevel: number;
  muted: boolean;
  pendingLLMRoute: "local" | "groq" | null;
}) {
  const userScale = 1 + (muted ? 0 : micLevel * 0.4);
  const aiScale = 1 + (agentState === "speaking" ? aiLevel * 0.35 : 0);
  return (
    <div className="flex items-center justify-center gap-10 md:gap-16 py-5 border-b border-ink-100/70 bg-paper/50">
      <AvatarOrb
        label={initials(client.name)}
        color={client.avatar_color || "#FFD6A5"}
        active={!muted && micLevel > 0.02}
        level={micLevel}
        scale={userScale}
        caption={muted ? "muted" : agentState === "thinking" ? "…" : "you"}
      />
      <motion.div
        className="text-ink-300 text-xs font-mono"
        animate={{ opacity: [0.35, 1, 0.35] }}
        transition={{ duration: 2.4, repeat: Infinity, ease: "easeInOut" }}
      >
        ···
      </motion.div>
      <AvatarOrb
        label="AI"
        color="#1F2227"
        textColor="#FFFFFF"
        active={agentState === "speaking"}
        level={aiLevel}
        scale={aiScale}
        caption={
          agentState === "speaking"
            ? "speaking"
            : agentState === "thinking"
              ? pendingLLMRoute === "local"
                ? "thinking · local"
                : pendingLLMRoute === "groq"
                  ? "thinking · groq"
                  : "thinking"
              : "listening"
        }
        variant="ai"
      />
    </div>
  );
}

function AvatarOrb({
  label,
  color,
  textColor,
  active,
  level,
  scale,
  caption,
  variant = "user",
}: {
  label: string;
  color: string;
  textColor?: string;
  active: boolean;
  level: number;
  scale: number;
  caption: string;
  variant?: "user" | "ai";
}) {
  return (
    <div className="flex flex-col items-center gap-2">
      <motion.div
        className="relative h-20 w-20 grid place-items-center"
        animate={{ scale }}
        transition={{ type: "spring", stiffness: 380, damping: 28, mass: 0.45 }}
      >
        {/* Pulse rings — opacity driven by active level */}
        <span
          className={clsx(
            "absolute inset-0 rounded-full blur-md transition-opacity duration-200",
            variant === "ai" ? "bg-accent/20" : "bg-ok/20",
          )}
          style={{ opacity: active ? 0.4 + Math.min(0.6, level * 2) : 0 }}
        />
        <span
          className={clsx(
            "absolute -inset-1 rounded-full border",
            variant === "ai" ? "border-accent/30" : "border-ok/30",
          )}
          style={{
            opacity: active ? 0.6 : 0.1,
            transform: `scale(${1 + level * 0.25})`,
            transition: "transform 80ms linear, opacity 120ms linear",
          }}
        />
        <motion.span
          className="relative h-16 w-16 rounded-full grid place-items-center font-display text-base shadow-soft ring-4 ring-white/60"
          style={{ background: color, color: textColor || "#1F2227" }}
          animate={
            active
              ? {
                  boxShadow:
                    variant === "ai"
                      ? [
                          "0 0 0 0 rgba(47,111,237,0)",
                          "0 0 0 12px rgba(47,111,237,0.14)",
                          "0 0 0 0 rgba(47,111,237,0)",
                        ]
                      : [
                          "0 0 0 0 rgba(31,169,113,0)",
                          "0 0 0 12px rgba(31,169,113,0.16)",
                          "0 0 0 0 rgba(31,169,113,0)",
                        ],
                }
              : { boxShadow: "0 1px 3px rgba(10,10,10,0.06)" }
          }
          transition={{ duration: active ? 1.8 : 0.25, repeat: active ? Infinity : 0, ease: "easeOut" }}
        >
          {label}
        </motion.span>
      </motion.div>
      <motion.span
        className="text-[11px] uppercase tracking-[0.12em] text-ink-400"
        key={caption}
        initial={{ opacity: 0.5, y: 2 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.22 }}
      >
        {caption}
      </motion.span>
    </div>
  );
}

function LiveUserHero({
  m,
  micLevel,
  muted,
}: {
  m: ChatMessage;
  micLevel: number;
  muted: boolean;
}) {
  const line = m.fullText.slice(0, m.revealed);
  const active = !muted && micLevel > 0.04;
  return (
    <div className="shrink-0 border-b border-ink-100 bg-gradient-to-r from-accent/[0.07] via-paper to-rose-500/[0.06]">
      <div className="max-w-4xl mx-auto px-4 py-5 md:px-8 md:py-6">
        <div className="flex items-center justify-between gap-3 mb-3">
          <span className="text-[10px] font-semibold tracking-[0.28em] text-accent uppercase">Live · streaming</span>
          {active ? (
            <span className="flex gap-1.5 items-center text-[10px] font-medium text-ok uppercase tracking-wider">
              <span className="inline-flex gap-0.5 items-end h-3.5">
                <span className="w-1 rounded-full bg-ok animate-pulse h-2" />
                <span className="w-1 rounded-full bg-ok/80 animate-pulse h-3 [animation-delay:100ms]" />
                <span className="w-1 rounded-full bg-ok/60 animate-pulse h-1.5 [animation-delay:200ms]" />
              </span>
              Speaking
            </span>
          ) : null}
        </div>
        <p className="font-display text-[clamp(1.25rem,3.2vw,1.85rem)] leading-snug text-ink-900 whitespace-pre-wrap break-words">
          {line || "…"}
          {m.revealed < m.fullText.length ? (
            <span className="inline-block w-0.5 h-[0.95em] ml-0.5 align-[-0.06em] bg-accent animate-pulse rounded-sm" />
          ) : null}
        </p>
        <p className="text-[11px] text-ink-400 mt-2">Words appear in order as the transcript updates.</p>
      </div>
    </div>
  );
}

function CallLobby({ state }: { state: TransportState }) {
  return (
    <div className="min-h-[min(380px,50vh)] flex flex-col items-center justify-center text-center px-4 py-10">
      <div className="max-w-md">
        <h1 className="font-display text-3xl md:text-4xl text-ink-800 mb-3">Ready to call</h1>
        <p className="text-ink-500 text-sm leading-relaxed">
          {state === "connecting"
            ? "Connecting the microphone…"
            : "Press Start call, allow the mic, then speak. Your words stream word-by-word in the live strip at the top."}
        </p>
      </div>
    </div>
  );
}

function MessageBubble({ m }: { m: ChatMessage }) {
  const isUser = m.speaker === "user";
  const baseText = useMemo(
    () => (isUser ? m.fullText : sanitizeAssistantVisibleText(m.fullText)),
    [isUser, m.fullText],
  );
  const reveal = useMemo(
    () => baseText.slice(0, Math.min(m.revealed, baseText.length)),
    [baseText, m.revealed],
  );
  const showCursor = isUser
    ? Boolean(m.pending && !m.fullText) ||
        Boolean(m.sttInterim && m.fullText.length > 0 && m.revealed < m.fullText.length)
    : Boolean(m.partial || m.revealed < m.fullText.length);
  const userInterim = isUser && (m.sttInterim || (m.partial && Boolean(m.fullText)));
  const asstStreaming = !isUser && m.partial && Boolean(m.fullText);
  return (
    <motion.li
      layout="position"
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: -6 }}
      transition={{ type: "spring", stiffness: 400, damping: 30 }}
      className={clsx("flex gap-2 items-end", isUser ? "justify-end" : "justify-start")}
    >
      {!isUser && <Avatar kind="assistant" />}
      <div className="max-w-[min(90%,32rem)] flex flex-col gap-1 items-start">
        {!isUser && m.llmRoute && (
          <span
            className={clsx(
              "text-[10px] font-mono uppercase tracking-wider rounded-full px-2 py-0.5 border",
              m.llmRoute === "local" ? "border-ok/30 bg-ok/10 text-ok" : "border-accent/30 bg-accent/10 text-accent",
            )}
          >
            {m.llmRoute === "local" ? "Local" : "Groq"}
          </span>
        )}
        <motion.div
          className={clsx(
            "w-full rounded-2xl px-4 py-2.5 text-[14px] leading-relaxed shadow-sm border",
            isUser
              ? clsx(
                  "bg-gradient-to-br from-accent to-rose-500 text-white border-transparent rounded-br-md",
                  userInterim && "text-white/75",
                )
              : clsx(
                  "bg-card border-ink-100 text-ink-800 rounded-bl-md",
                  asstStreaming && "text-ink-500",
                ),
          )}
        >
          {m.pending && !m.fullText ? (
            <TypingDots />
          ) : (
            <>
              <span className="whitespace-pre-wrap break-words">{reveal}</span>
              {showCursor && <span className="inline-block w-0.5 h-[1em] ml-0.5 align-[-0.08em] bg-current opacity-45 animate-pulse rounded-sm" />}
            </>
          )}
        </motion.div>
      </div>
      {isUser && <Avatar kind="user" />}
    </motion.li>
  );
}

function Avatar({ kind }: { kind: "user" | "assistant" }) {
  return (
    <span
      className={clsx(
        "h-7 w-7 shrink-0 rounded-full grid place-items-center text-[11px] font-display shadow-soft mt-0.5",
        kind === "assistant" ? "bg-ink-800 text-paper" : "bg-accent text-white",
      )}
    >
      {kind === "assistant" ? "AI" : "You"}
    </span>
  );
}

function TypingDots() {
  return (
    <span className="inline-flex items-center gap-1 py-1">
      <span className="h-1.5 w-1.5 rounded-full bg-ink-400 animate-bounce [animation-delay:-0.2s]" />
      <span className="h-1.5 w-1.5 rounded-full bg-ink-400 animate-bounce [animation-delay:-0.1s]" />
      <span className="h-1.5 w-1.5 rounded-full bg-ink-400 animate-bounce" />
    </span>
  );
}

function StatusPill({ state, agent }: { state: TransportState; agent: AgentState }) {
  const label = state !== "live" ? state : agent;
  const color =
    state !== "live"
      ? "bg-ink-100 text-ink-500"
      : agent === "speaking"
        ? "bg-accent/10 text-accent"
        : agent === "thinking"
          ? "bg-warn/10 text-warn"
          : "bg-ok/10 text-ok";
  return (
    <motion.span
      layout
      key={label}
      initial={{ opacity: 0.6, scale: 0.96 }}
      animate={{ opacity: 1, scale: 1 }}
      transition={{ type: "spring", stiffness: 500, damping: 34 }}
      className={clsx("px-2.5 py-0.5 rounded-full text-[11px] capitalize", color)}
    >
      {label}
    </motion.span>
  );
}

function prettyAction(t: string) {
  return t.replaceAll("_", " ");
}

// ---------- message helpers ----------

// appendAssistantDelta merges streaming tokens into the last assistant bubble.
// If the last bubble is a pending "thinking" placeholder it promotes it in-place.
function appendAssistantDelta(msgs: ChatMessage[], delta: string): ChatMessage[] {
  const last = msgs[msgs.length - 1];
  if (last && last.speaker === "assistant" && last.partial) {
    const nextFull = sanitizeAssistantVisibleText(last.fullText + delta);
    const copy = msgs.slice();
    copy[copy.length - 1] = {
      ...last,
      fullText: nextFull,
      revealed: nextFull.length,
      pending: false,
      partial: true,
      llmRoute: last.llmRoute,
    };
    return copy;
  }
  const clean = sanitizeAssistantVisibleText(delta);
  return [
    ...msgs,
    {
      id: nextMsgID(),
      speaker: "assistant",
      fullText: clean,
      revealed: clean.length,
      t: Date.now(),
      pending: false,
      partial: true,
    },
  ];
}

// Live STT: server extends fullText; keep revealed behind so the word interval animates.
// Never set revealed = fullText.length here — that would skip word-by-word display.
function updateUserPartialCaption(msgs: ChatMessage[], text: string): ChatMessage[] {
  const last = msgs[msgs.length - 1];
  if (last && last.speaker === "user" && (last.pending || last.partial)) {
    let revealed = last.revealed;
    if (text.length < revealed) revealed = text.length;
    revealed = Math.min(revealed, text.length);
    const copy = msgs.slice();
    copy[copy.length - 1] = {
      ...last,
      fullText: text,
      revealed,
      pending: false,
      partial: true,
      sttInterim: true,
    };
    return copy;
  }
  return [
    ...msgs,
    {
      id: nextMsgID(),
      speaker: "user",
      fullText: text,
      revealed: 0,
      t: Date.now(),
      pending: false,
      partial: true,
      sttInterim: true,
    },
  ];
}

// finalizeMessage is used for final transcripts. It replaces the last pending
// bubble from the same speaker if present, otherwise appends a fresh bubble.
// The text is set as fullText and the typewriter takes over from the current
// revealed count.
function finalizeMessage(
  msgs: ChatMessage[],
  speaker: "user" | "assistant",
  text: string,
  assistantRoute?: "local" | "groq",
): ChatMessage[] {
  const safe =
    speaker === "assistant" ? sanitizeAssistantVisibleText(text) : text;
  const last = msgs[msgs.length - 1];
  if (last && last.speaker === speaker && (last.pending || last.partial)) {
    if (!safe) return msgs.slice(0, -1);
    const copy = msgs.slice();
    const route =
      speaker === "assistant" ? assistantRoute ?? last.llmRoute : undefined;
    copy[copy.length - 1] = {
      ...last,
      fullText: safe,
      // Final user line: show full text immediately; assistant keeps typewriter from current revealed.
      revealed: speaker === "user" ? safe.length : Math.min(last.revealed, safe.length),
      pending: false,
      partial: false,
      sttInterim: speaker === "user" ? false : undefined,
      llmRoute: speaker === "assistant" ? route : undefined,
    };
    return copy;
  }
  if (!safe) return msgs;
  return [
    ...msgs,
    {
      id: nextMsgID(),
      speaker,
      fullText: safe,
      revealed: 0,
      t: Date.now(),
      partial: false,
      sttInterim: speaker === "user" ? false : undefined,
      llmRoute: speaker === "assistant" ? assistantRoute : undefined,
    },
  ];
}

function dropPending(msgs: ChatMessage[]): ChatMessage[] {
  let end = msgs.length;
  while (end > 0) {
    const m = msgs[end - 1];
    if (m.pending && !m.fullText) {
      end--;
      continue;
    }
    break;
  }
  return end === msgs.length ? msgs : msgs.slice(0, end);
}

function dropPendingUser(msgs: ChatMessage[]): ChatMessage[] {
  const last = msgs[msgs.length - 1];
  if (last && last.speaker === "user" && last.pending && !last.fullText) {
    return msgs.slice(0, -1);
  }
  return msgs;
}

function initials(s: string) {
  const p = s.trim().split(/\s+/);
  return (p[0]?.[0] || "?") + (p[1]?.[0] || "");
}

function useClock() {
  const [t, setT] = useState(Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setT(Date.now()), 500);
    return () => window.clearInterval(id);
  }, []);
  return t;
}

function msToClock(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}
