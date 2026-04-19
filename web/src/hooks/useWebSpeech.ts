import { useCallback, useRef, useState } from "react";

interface WebSpeechRecognitionEvent extends Event {
  readonly resultIndex: number;
  readonly results: unknown;
}

interface WebSpeechRecognitionErrorEvent extends Event {
  readonly error: string;
  readonly message: string;
}

interface WebRecognition {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onstart: ((this: WebRecognition, ev: Event) => void) | null;
  onend: ((this: WebRecognition, ev: Event) => void) | null;
  onresult: ((this: WebRecognition, ev: WebSpeechRecognitionEvent) => void) | null;
  onerror: ((this: WebRecognition, ev: WebSpeechRecognitionErrorEvent) => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

declare global {
  interface Window {
    SpeechRecognition?: { new (): WebRecognition };
    webkitSpeechRecognition?: { new (): WebRecognition };
  }
}

export interface UseWebSpeechReturn {
  isSupported: boolean;
  isListening: boolean;
  transcript: string;
  interimTranscript: string;
  error: string | null;
  start: () => void;
  stop: () => void;
  reset: () => void;
  /** Called with each finalized phrase from the engine (trimmed). */
  setOnFinalPhrase: (handler: ((phrase: string) => void) | null) => void;
}

export function useWebSpeech(): UseWebSpeechReturn {
  const [isListening, setIsListening] = useState(false);
  const [transcript, setTranscript] = useState("");
  const [interimTranscript, setInterimTranscript] = useState("");
  const [error, setError] = useState<string | null>(null);
  const recognitionRef = useRef<WebRecognition | null>(null);
  /** True between start() and stop(); onend only auto-restarts while this is true. */
  const sessionActiveRef = useRef(false);
  const onFinalPhraseRef = useRef<((phrase: string) => void) | null>(null);

  const isSupported = Boolean(
    typeof window !== "undefined" && (window.SpeechRecognition || window.webkitSpeechRecognition),
  );

  const setOnFinalPhrase = useCallback((handler: ((phrase: string) => void) | null) => {
    onFinalPhraseRef.current = handler;
  }, []);

  const start = useCallback(() => {
    if (!isSupported) return;

    try {
      const SpeechRecognitionClass = window.SpeechRecognition || window.webkitSpeechRecognition;
      if (!SpeechRecognitionClass) return;

      sessionActiveRef.current = true;

      const recognition = new SpeechRecognitionClass();
      recognition.continuous = true;
      recognition.interimResults = true;
      recognition.lang = "en-US";

      recognition.onstart = () => {
        setIsListening(true);
        setError(null);
      };

      recognition.onend = () => {
        setIsListening(false);
        if (recognitionRef.current !== recognition || !sessionActiveRef.current) return;
        window.setTimeout(() => {
          if (recognitionRef.current !== recognition || !sessionActiveRef.current) return;
          try {
            recognition.start();
          } catch {
            /* already running */
          }
        }, 120);
      };

      recognition.onresult = (event: WebSpeechRecognitionEvent) => {
        let newFinalChunk = "";
        let interim = "";

        const results = event.results as { length: number; [i: number]: { isFinal: boolean; 0: { transcript: string } } };
        for (let i = event.resultIndex; i < results.length; i++) {
          const result = results[i];
          const piece = result[0]?.transcript || "";
          if (result.isFinal) {
            newFinalChunk += piece;
          } else {
            interim += piece;
          }
        }

        if (newFinalChunk.trim()) {
          const trimmed = newFinalChunk.trim();
          setTranscript((prev) => {
            const base = prev.trimEnd();
            return base ? `${base} ${trimmed} ` : `${trimmed} `;
          });
          onFinalPhraseRef.current?.(trimmed);
        }
        setInterimTranscript(interim);
      };

      recognition.onerror = (event: WebSpeechRecognitionErrorEvent) => {
        console.error("Speech recognition error:", event.error);
        setError(event.error);

        if (event.error === "no-speech" || event.error === "aborted") {
          return;
        }

        if (!sessionActiveRef.current) return;

        window.setTimeout(() => {
          if (recognitionRef.current !== recognition || !sessionActiveRef.current) return;
          try {
            recognition.start();
          } catch {
            /* ignore */
          }
        }, 400);
      };

      recognition.start();
      recognitionRef.current = recognition;
    } catch (e) {
      console.error("Failed to start speech recognition:", e);
      setError(String(e));
      sessionActiveRef.current = false;
    }
  }, [isSupported]);

  const stop = useCallback(() => {
    sessionActiveRef.current = false;
    const r = recognitionRef.current;
    recognitionRef.current = null;
    if (r) {
      try {
        r.stop();
      } catch {
        /* already stopped */
      }
    }
    setIsListening(false);
  }, []);

  const reset = useCallback(() => {
    setTranscript("");
    setInterimTranscript("");
    setError(null);
  }, []);

  return {
    isSupported,
    isListening,
    transcript,
    interimTranscript,
    error,
    start,
    stop,
    reset,
    setOnFinalPhrase,
  };
}
