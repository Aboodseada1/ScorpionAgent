import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import {
  Phone,
  PhoneOff,
  Mic,
  MicOff,
  Send,
  Sparkles,
  AlertTriangle,
  X,
  Check,
  Loader2,
  Mic2,
  Volume2,
  VolumeX,
} from "lucide-react";
import clsx from "clsx";
import { AnimatePresence, motion } from "framer-motion";
import { api, type Action, type Client } from "../lib/api";
import { sanitizeAssistantVisibleText } from "../lib/assistantSanitize";
import { CallTransport } from "../lib/rtc";
import { useWebSpeech } from "../hooks/useWebSpeech";

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

/** User live STT: balanced tick for smooth word-by-word display */
const STT_WORD_MS = 16;  // ~60fps for smooth animation

/** After TTS ends, keep Web Speech off briefly so room/speaker tail is not transcribed. */
const POST_TTS_SPEECH_COOLDOWN_MS = 3000; // EXTENDED to 3 seconds for maximum echo protection

const PLAY_ASSISTANT_VOICE_KEY = "scorpion_call_play_assistant_voice";

function readPlayAssistantVoicePref(): boolean {
  try {
    return typeof localStorage !== "undefined" && localStorage.getItem(PLAY_ASSISTANT_VOICE_KEY) === "1";
  } catch {
    return false;
  }
}

function writePlayAssistantVoicePref(on: boolean): void {
  try {
    localStorage.setItem(PLAY_ASSISTANT_VOICE_KEY, on ? "1" : "0");
  } catch {
    /* noop */
  }
}

function normalizeUtterance(s: string): string {
  return s
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9\s]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

/** True if the browser transcript is probably the assistant line played through speakers. */
function utteranceLooksLikeTtsEcho(user: string, assistant: string, timeSinceAssistant: number): boolean {
  const u = normalizeUtterance(user);
  const a = normalizeUtterance(assistant);
  if (!u || !a) return false;

  // NUCLEAR OPTION: Block ANY text that appears during/after assistant speaks
  if (timeSinceAssistant < 3000) { // 3 seconds after assistant spoke
    return true; // BLOCK EVERYTHING for 3 seconds after TTS
  }

  // Exact match - definitely echo
  if (u === a) return true;

  // ANY overlap for short texts = echo (super aggressive)
  if (u.length <= 10 || a.length <= 10) {
    const overlapRatio = calculateOverlapRatio(u, a);
    return overlapRatio >= 0.3; // 30% overlap for short texts
  }

  // Medium texts - be strict
  if (u.length <= 20) {
    const overlapRatio = calculateOverlapRatio(u, a);
    return overlapRatio >= 0.25; // 25% overlap
  }

  // Long texts - moderate but still strict
  const overlapRatio = calculateOverlapRatio(u, a);
  return overlapRatio >= 0.2; // 20% overlap
}

function calculateOverlapRatio(user: string, assistant: string): number {
  const uw = user.split(" ").filter(Boolean);
  const aw = new Set(assistant.split(" ").filter(Boolean));

  if (uw.length === 0 || aw.size === 0) return 0;

  let inter = 0;
  for (const w of uw) {
    if (aw.has(w)) inter++;
  }

  const union = new Set([...uw, ...aw]).size;
  if (union === 0) return 0;

  return inter / union;
}

function revealNextWordChunk(fullText: string, revealed: number): number {
  if (revealed >= fullText.length) return revealed;
  const slice = fullText.slice(revealed);
  const lead = slice.match(/^(\s+)/);
  if (lead) return revealed + lead[1].length;
  const word = slice.match(/^(\S+)(\s*)/);
  if (word) return revealed + word[1].length + word[2].length;
  return revealed + 1;
}

/** Balanced word reveal speed - smooth but responsive */
function sttWordsPerTick(fullLen: number, revealed: number): number {
  const backlog = fullLen - revealed;
  if (backlog <= 0) return 0;
  if (backlog > 30) return 5;  // Fast when behind
  if (backlog > 15) return 3;  // Medium speed
  if (backlog > 5) return 2;   // Slower for small chunks
  return 1;  // One word at a time for final polish
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
  const [echoFreezeMode, setEchoFreezeMode] = useState(true); // HARD FREEZE echo blocking by default
  /** Piper → tab speakers. Default off: laptop speakers + Web Speech = echo. Headphones: turn on. */
  const [playAssistantVoice, setPlayAssistantVoice] = useState(readPlayAssistantVoicePref);
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const [typing, setTyping] = useState("");
  const [micLevel, setMicLevel] = useState(0);
  const [aiLevel, setAILevel] = useState(0);
  const [echoFilteredCount, setEchoFilteredCount] = useState(0); // Track echo filtering
  const [preload, setPreload] = useState<Record<WarmupKey, { status: PreloadState; ms?: number; err?: string }>>({
    llm: { status: "pending" },
    tts: { status: "pending" },
    stt: { status: "ok" },
  });
  const [preloading, setPreloading] = useState(false);
  // Chrome Web Speech API is now the only STT method
  const transport = useRef<CallTransport | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  // Chrome Web Speech API integration (primary STT method)
  const webSpeech = useWebSpeech();
  const lastWebSpeechRef = useRef<string>("");
  /** Last finalized assistant text (for echo detection before send). */
  const lastAssistantTextRef = useRef<string>("");
  const lastAssistantTimeRef = useRef<number>(0); // Track when assistant last spoke
  const speechResumeAtRef = useRef(0);
  const [speechGateEpoch, setSpeechGateEpoch] = useState(0);

  /** Show all messages including live STT - no more separate ugly live caption */
  const { threadMessages } = useMemo(() => {
    return { threadMessages: messages };
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

  const { isSupported: speechSupported, start: speechStart, stop: speechStop, reset: speechReset, setOnFinalPhrase } =
    webSpeech;

  // Mic dictation: AGGRESSIVELY off during TTS and for extended cooldown (speaker tail / room echo).
  useEffect(() => {
    if (state !== "live" || !speechSupported) {
      speechStop();
      return;
    }
    // COMPLETELY BLOCK speech while assistant is speaking
    if (agentState === "speaking") {
      speechStop();
      return;
    }

    // EXTENDED COOLDOWN: Block for longer after assistant stops
    const waitMs = speechResumeAtRef.current - Date.now();
    if (waitMs > 0) {
      speechStop();
      const t = window.setTimeout(() => setSpeechGateEpoch((n) => n + 1), waitMs);
      return () => window.clearTimeout(t);
    }
    speechStart();
    return () => {
      speechStop();
    };
  }, [state, agentState, speechSupported, speechStart, speechStop, speechGateEpoch]);

  useEffect(() => {
    transport.current?.setAssistantSpeakerOutput(playAssistantVoice);
  }, [playAssistantVoice]);

  // Each Web Speech final phrase → backend (was broken: ref compared after it was overwritten).
  useEffect(() => {
    setOnFinalPhrase((phrase) => {
      const t = phrase.trim();

      // NUCLEAR FREEZE: Completely block until manually released
      if (echoFreezeMode) {
        // Show user that echo protection is active
        pushNotice("echo-freeze", "Echo protection active - use text input or headphones", "warn");
        return; // COMPLETELY BLOCK ALL SPEECH
      }

      // NUCLEAR: Block everything during cooldown period
      const timeSinceAssistant = Date.now() - lastAssistantTimeRef.current;
      if (timeSinceAssistant < 4000) { // 4 seconds after assistant spoke
        lastWebSpeechRef.current = "";
        speechReset();
        setEchoFilteredCount(prev => prev + 1);
        return; // COMPLETELY BLOCK during cooldown
      }

      // Very short texts are often noise/echo artifacts
      if (!t || t.length < 3 || !transport.current) return;

      // Check against echo with aggressive thresholds
      if (utteranceLooksLikeTtsEcho(t, lastAssistantTextRef.current, timeSinceAssistant)) {
        lastWebSpeechRef.current = "";
        speechReset();
        setEchoFilteredCount(prev => prev + 1); // Track filtered echoes
        // Show user notice about echo filtering
        pushNotice("echo", "Filtered echo (assistant's voice was detected)", "warn");
        return;
      }

      // Additional protection: reject very repetitive phrases (common in echo loops)
      const recentUserTexts = messages
        .filter(m => m.speaker === "user" && !m.partial)
        .slice(-3)
        .map(m => normalizeUtterance(m.fullText));

      if (recentUserTexts.length >= 2) {
        const isRepetitive = recentUserTexts.every(
          recentText => normalizeUtterance(t) === recentText ||
                       (t.length > 5 && recentText.includes(t))
        );
        if (isRepetitive) {
          lastWebSpeechRef.current = "";
          speechReset();
          setEchoFilteredCount(prev => prev + 1);
          pushNotice("repetitive", "Filtered repetitive text (possible echo)", "warn");
          return;
        }
      }

      transport.current.sendText(t);
      lastWebSpeechRef.current = "";
      setMessages((ms) => {
        const last = ms[ms.length - 1];
        if (last && last.speaker === "user" && last.partial) {
          const copy = [...ms];
          copy[copy.length - 1] = {
            ...last,
            fullText: t,
            revealed: t.length,
            partial: false,
            sttInterim: false,
          };
          return copy;
        }
        return ms;
      });
      speechReset();
    });
    return () => setOnFinalPhrase(null);
  }, [setOnFinalPhrase, speechReset, messages, echoFreezeMode]);

  // Sync Web Speech API results with chat messages
  useEffect(() => {
    if (state !== "live") return;

    const combinedText = webSpeech.transcript + " " + webSpeech.interimTranscript;
    const trimmedText = combinedText.trim();

    if (!trimmedText || trimmedText === lastWebSpeechRef.current) return;

    lastWebSpeechRef.current = trimmedText;

    setMessages((ms) => {
      const last = ms[ms.length - 1];

      // Update existing user message or create new one
      if (last && last.speaker === "user" && (last.pending || last.partial)) {
        const copy = [...ms];
        copy[copy.length - 1] = {
          ...last,
          fullText: trimmedText,
          revealed: trimmedText.length, // Show all text immediately
          pending: false,
          partial: true,
          sttInterim: true,
        };
        return copy;
      }

      // Create new message if no existing user message
      return [
        ...ms,
        {
          id: nextMsgID(),
          speaker: "user",
          fullText: trimmedText,
          revealed: trimmedText.length,
          t: Date.now(),
          pending: false,
          partial: true,
          sttInterim: true,
        },
      ];
    });
  }, [webSpeech.transcript, webSpeech.interimTranscript, state]);

  // Cleanup: remove empty bubbles after reasonable timeout
  useEffect(() => {
    const hasStale = messages.some((m) => m.pending && !m.fullText);
    if (!hasStale) return;
    const handle = window.setTimeout(() => {
      setMessages((ms) => ms.filter((m) => !(m.pending && !m.fullText && Date.now() - m.t > 2000)));
    }, 2500);  // Remove empty bubbles after 4.5 seconds total
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
    lastAssistantTextRef.current = "";
    setActions([]);
    setNotices([]);
    setLatency({});
    setAgentState("idle");
    setPendingLLMRoute(null);
    llmStartAt.current = null;

    // ---- Phase 1: warmup every backing service in parallel. This makes the
    // first turn feel instant (LLM kv-cache hot, piper paged into RAM).
    setPreloading(true);
    setPreload({ llm: { status: "running" }, tts: { status: "running" }, stt: { status: "ok" } });
    try {
      const res = await api.warmupSession();
      const next: typeof preload = { llm: { status: "pending" }, tts: { status: "pending" }, stt: { status: "ok" } };
      (["llm", "tts"] as const).forEach((k) => {
        const s = res.status[k];
        if (!s) return;
        next[k] = { status: s.ok ? "ok" : "failed", ms: s.ms, err: s.err };
      });
      setPreload(next);
      if (!res.ok) {
        pushNotice(
          "warmup",
          Object.entries(res.status)
            .filter(([, v]) => v && !v.ok)
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
      await t.start(session_id, { playAssistantAudio: playAssistantVoice });
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
          // Only create new bubble if last one is finalized or doesn't exist
          if (last && last.speaker === "user" && last.pending) {
            // Update timestamp of existing pending bubble
            const copy = [...m];
            copy[copy.length - 1] = { ...last, t: Date.now() };
            return copy;
          }
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
      case "utterance_empty": {
        setMessages((m) => dropPendingUser(m));
        break;
      }
      case "self_filtered": {
        const filtered = String(e.payload?.text || "").trim();
        setMessages((m) => {
          const next = dropPendingUser(m);
          const last = next[next.length - 1];
          if (!last || last.speaker !== "user") return next;
          const prevAssistant = [...next.slice(0, -1)].reverse().find((x) => x.speaker === "assistant");
          const drop =
            (filtered && last.fullText.trim().toLowerCase() === filtered.toLowerCase()) ||
            Boolean(prevAssistant && utteranceLooksLikeTtsEcho(last.fullText, prevAssistant.fullText, 99999)); // Use large time for message display check
          return drop ? next.slice(0, -1) : next;
        });
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
        if (speaker === "assistant" && text.trim()) {
          lastAssistantTextRef.current = text;
        }
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
        lastAssistantTimeRef.current = Date.now(); // Track when assistant finished speaking
        speechResumeAtRef.current = Date.now() + POST_TTS_SPEECH_COOLDOWN_MS;
        setSpeechGateEpoch((n) => n + 1);
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

    

  if (!client) {
    return (
      <div className="flex-1 min-h-0 flex items-center justify-center bg-paper text-ink-400">
        <p className="text-sm">Loading…</p>
      </div>
    );
  }

  const inPreload = preloading && state !== "live";
  const isLive = state === "live";

  return (
    <motion.div
      className="flex-1 min-h-0 flex flex-col overflow-hidden bg-gradient-to-br from-paper via-paper to-[#FFF6EE] text-ink-800 font-sans"
      initial={{ opacity: 0.92 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.35 }}
    >
      

      {inPreload ? (
        <PreloadScreen preload={preload} />
      ) : (
        <div className="flex-1 min-h-0 flex flex-col lg:flex-row overflow-hidden">
          <div className="flex-1 min-h-0 flex flex-col min-w-0 lg:border-r border-ink-100/80 bg-paper/40">
            <div
              ref={scrollRef}
              className="flex-1 min-h-0 overflow-y-auto px-4 py-4 md:px-8 md:py-5 grid-dots flex flex-col"
            >
              {threadMessages.length === 0 ? (
                <div className="flex-1 min-h-[12rem] flex flex-col items-center justify-center gap-3 px-4 text-center">
                  <p className="text-ink-500 text-sm max-w-md leading-relaxed">
                    {state === "connecting" ? "Connecting…" : "Speak to start the conversation"}
                  </p>
                  {state !== "live" && state !== "connecting" && (
                    <p className="text-ink-400 text-xs max-w-sm leading-relaxed">
                      Tap <span className="font-medium text-ink-600">Start call</span>, then use your microphone or the field below.
                    </p>
                  )}
                </div>
              ) : (
                <div className="flex-1 min-h-0 flex flex-col">
                  <div className="min-h-full flex flex-1 flex-col justify-end py-2">
                    <ul className="space-y-3 max-w-2xl mx-auto w-full pb-3">
                      <AnimatePresence initial={false}>
                        {threadMessages.map((m) => (
                          <MessageBubble key={m.id} m={m} />
                        ))}
                      </AnimatePresence>
                    </ul>
                  </div>
                </div>
              )}
            </div>

            <div className="shrink-0 relative z-10 flex items-center gap-3 px-4 py-3.5 md:px-6 border-t border-ink-100 bg-card/95 backdrop-blur-sm shadow-[0_-10px_40px_rgba(26,26,23,0.06)]">
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
                    onClick={() => {
                      setPlayAssistantVoice((v) => {
                        const next = !v;
                        writePlayAssistantVoicePref(next);
                        return next;
                      });
                    }}
                    className={clsx(
                      "h-11 w-11 rounded-full grid place-items-center shrink-0 transition shadow-soft border",
                      playAssistantVoice
                        ? "bg-ok/15 text-ok border-ok/35"
                        : "bg-paper text-ink-500 border-ink-100 hover:border-ink-300",
                    )}
                    title={
                      playAssistantVoice
                        ? "Assistant voice on (Piper to speakers). Turn off on laptop speakers to stop echo."
                        : "Assistant voice off — Piper not sent to speakers (stops mic picking up the AI). Use with headphones to turn on."
                    }
                  >
                    {playAssistantVoice ? <Volume2 size={17} /> : <VolumeX size={17} />}
                  </button>

                  <button
                    type="button"
                    onClick={hangup}
                    className="h-11 w-11 rounded-full bg-bad text-white grid place-items-center shrink-0 hover:opacity-95 transition shadow-soft"
                    title="Hang up"
                  >
                    <PhoneOff size={17} />
                  </button>

                  {/* Echo Protection Toggle */}
                  <button
                    type="button"
                    onClick={() => setEchoFreezeMode(!echoFreezeMode)}
                    className={clsx(
                      "h-11 w-11 rounded-full grid place-items-center shrink-0 transition shadow-soft border",
                      echoFreezeMode
                        ? "bg-bad/10 text-bad border-bad/30 hover:bg-bad/20"
                        : "bg-ok/10 text-ok border-ok/30 hover:bg-ok/20",
                    )}
                    title={echoFreezeMode ? "Echo Protection: ON (speech blocked)" : "Echo Protection: OFF"}
                  >
                    <AlertTriangle size={17} />
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

          <aside className="lg:w-[min(400px,38vw)] w-full shrink-0 flex flex-col min-h-0 border-t lg:border-t-0 lg:border-l border-ink-100 bg-card/80 lg:bg-gradient-to-b lg:from-card/90 lg:to-paper/60">
            {isLive && (
              <div className="shrink-0 border-b border-ink-100/80">
                <VoicePanel
                  client={client}
                  agentState={agentState}
                  micLevel={micLevel}
                  aiLevel={aiLevel}
                  muted={muted}
                  pendingLLMRoute={pendingLLMRoute}
                  speechListening={webSpeech.isListening}
                />
              </div>
            )}

            <div className="flex-1 min-h-0 overflow-y-auto overscroll-contain p-4 md:p-5 space-y-4">
              {/* Chrome Web Speech API Status - Always Enabled */}
              {webSpeech.isSupported && (
                <div className="rounded-2xl border border-ink-100 bg-paper/80 p-4 text-xs">
                  <div className="flex items-center gap-2 text-sm font-display text-ink-600 mb-2">
                    <Mic2 size={16} className="text-ok" />
                    Browser speech
                  </div>
                  <div className="flex items-center gap-2 mb-2">
                    <div
                      className={clsx(
                        "h-2 w-2 rounded-full",
                        agentState === "speaking"
                          ? "bg-ink-300"
                          : webSpeech.isListening
                            ? "bg-ok animate-pulse"
                            : "bg-ink-300",
                      )}
                    />
                    <span className="text-ink-600">
                      {agentState === "speaking"
                        ? "Blocked (assistant speaking)"
                        : webSpeech.isListening
                          ? "Listening for you"
                          : "Starting…"}
                    </span>
                  </div>

                  {/* Echo filtering indicator */}
                  {echoFreezeMode && (
                    <div className="flex items-center gap-2 mb-2 p-2 bg-red-50 rounded-lg border border-red-200">
                      <AlertTriangle size={12} className="text-red-600" />
                      <span className="text-red-700 font-medium">
                        Echo Protection: ON (speech blocked)
                      </span>
                    </div>
                  )}

                  {!echoFreezeMode && echoFilteredCount > 0 && (
                    <div className="flex items-center gap-2 mb-2">
                      <AlertTriangle size={12} className="text-warn" />
                      <span className="text-ink-600">
                        Filtered {echoFilteredCount} echo{echoFilteredCount !== 1 ? "es" : ""}
                      </span>
                    </div>
                  )}

                  {webSpeech.error && (
                    <div className="text-warn text-[11px] mt-1">Error: {webSpeech.error}</div>
                  )}

                  {/* Headphone recommendation */}
                  <div className="mt-3 p-2 bg-blue-50 rounded-lg border border-blue-100">
                    <div className="flex items-start gap-2">
                      <Volume2 size={12} className="text-blue-600 mt-0.5 shrink-0" />
                      <div className="text-[11px] text-ink-700 leading-tight">
                        <strong className="text-blue-800">Best experience:</strong> Use headphones or move
                        speakers away from mic to prevent the assistant from hearing itself.
                      </div>
                    </div>
                  </div>

                  {state === "live" && agentState !== "speaking" && webSpeech.isListening && (
                    <div className="text-ink-500 text-[11px] mt-1 space-y-1">
                      <p>Pause after a phrase to send; or type in the bar below.</p>
                      {!playAssistantVoice && (
                        <p className="text-ink-400">
                          Piper is not played through the tab speakers by default (echo-safe). Use the speaker button in
                          the bar below if you are on headphones.
                        </p>
                      )}
                    </div>
                  )}
                </div>
              )}

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
    { key: "stt", label: "Transcriber", hint: "Web Speech (this browser)" },
  ];
  return (
    <motion.div
      className="flex-1 min-h-0 grid place-items-center px-6 py-10 bg-gradient-to-br from-paper to-[#FFF6EE]"
      initial={{ opacity: 0.9 }}
      animate={{ opacity: 1 }}
    >
      <div className="max-w-lg w-full mx-auto flex flex-col items-center text-center">
        <div className="flex justify-center w-full mb-6">
          <motion.div
            className="flex h-16 w-16 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-accent to-ok text-white shadow-soft"
            animate={{ scale: [1, 1.04, 1] }}
            transition={{ duration: 2, repeat: Infinity, ease: "easeInOut" }}
          >
            <Phone size={26} className="shrink-0" strokeWidth={2} aria-hidden />
          </motion.div>
        </div>
        <div className="font-display text-2xl md:text-3xl text-ink-800 mb-2 w-full">Warming up the line</div>
        <p className="text-ink-500 text-sm mb-8 w-full max-w-md">
          LLM, voice, and speech services — then you are live.
        </p>
        <ul className="space-y-2 w-full max-w-md text-left">
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
  speechListening,
}: {
  client: Client;
  agentState: AgentState;
  micLevel: number;
  aiLevel: number;
  muted: boolean;
  pendingLLMRoute: "local" | "groq" | null;
  speechListening: boolean;
}) {
  const userScale = 1 + (muted ? 0 : micLevel * 0.4);
  const aiScale = 1 + (agentState === "speaking" ? aiLevel * 0.35 : 0);
  const userHot = !muted && micLevel > 0.04;
  const userCaption = muted ? "muted" : userHot ? "speaking" : "you";

  let aiCaption: string;
  if (agentState === "speaking") aiCaption = "speaking";
  else if (agentState === "thinking") {
    aiCaption =
      pendingLLMRoute === "local" ? "thinking · local" : pendingLLMRoute === "groq" ? "thinking · groq" : "thinking";
  } else if (userHot) aiCaption = "hearing you";
  else if (speechListening) aiCaption = "your turn";
  else aiCaption = "ready";

  return (
    <div className="flex items-center justify-center gap-8 md:gap-14 py-4 md:py-5 bg-paper/50">
      <AvatarOrb
        label={initials(client.name)}
        color={client.avatar_color || "#FFD6A5"}
        active={userHot}
        level={micLevel}
        scale={userScale}
        caption={userCaption}
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
        caption={aiCaption}
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
      initial={{ opacity: 0, y: 8, scale: 0.95 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      exit={{ opacity: 0, y: -6, scale: 0.95 }}
      transition={{ type: "spring", stiffness: 400, damping: 30 }}
      className={clsx("flex w-full", isUser ? "justify-end" : "justify-start")}
    >
      <div className="max-w-[min(85%,28rem)] flex flex-col gap-1 items-start">
        {!isUser && m.llmRoute && (
          <span
            className={clsx(
              "text-[10px] font-mono uppercase tracking-wider rounded-full px-2 py-0.5 border self-start",
              m.llmRoute === "local" ? "border-ok/30 bg-ok/10 text-ok" : "border-accent/30 bg-accent/10 text-accent",
            )}
          >
            {m.llmRoute === "local" ? "Local" : "Groq"}
          </span>
        )}
        <motion.div
          className={clsx(
            "w-full rounded-[20px] px-4 py-2.5 text-[15px] leading-relaxed shadow-sm",
            isUser
              ? clsx(
                  "bg-[#007AFF] text-white rounded-br-sm",
                  userInterim && "opacity-90",
                )
              : clsx(
                  "bg-white border border-ink-100 text-ink-900 rounded-bl-sm",
                  asstStreaming && "text-ink-600",
                ),
          )}
        >
          {m.pending && !m.fullText ? (
            <TypingDots />
          ) : (
            <>
              <span className="whitespace-pre-wrap break-words">{reveal}</span>
              {showCursor && <span className="inline-block w-0.5 h-[1em] ml-0.5 align-[-0.08em] bg-current opacity-60 animate-pulse rounded-sm" />}
            </>
          )}
        </motion.div>
      </div>
    </motion.li>
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

// Balanced display: show text with smooth word-by-word animation
function updateUserPartialCaption(msgs: ChatMessage[], text: string): ChatMessage[] {
  const last = msgs[msgs.length - 1];
  if (last && last.speaker === "user" && (last.pending || last.partial)) {
    let revealed = last.revealed;
    // Gradually reveal more text for smooth animation
    if (text.length > revealed) {
      revealed = Math.min(revealed + Math.ceil((text.length - revealed) * 0.4), text.length);
    } else {
      revealed = text.length;
    }
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
      revealed: Math.ceil(text.length * 0.6), // Reveal most immediately
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


