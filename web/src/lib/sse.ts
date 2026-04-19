import { useEffect, useRef, useState } from "react";

// useEventSource subscribes to an SSE endpoint and keeps the most recent
// decoded message of type T. Survives hot reloads and auto-reconnects on
// unexpected close.
export function useEventSource<T>(url: string | null): { last: T | null; connected: boolean } {
  const [last, setLast] = useState<T | null>(null);
  const [connected, setConnected] = useState(false);
  const retryRef = useRef<number>(0);

  useEffect(() => {
    if (!url) return;
    let alive = true;
    let es: EventSource | null = null;

    const connect = () => {
      if (!alive) return;
      es = new EventSource(url, { withCredentials: false });
      es.onopen = () => {
        if (!alive) return;
        retryRef.current = 0;
        setConnected(true);
      };
      es.onerror = () => {
        if (!alive) return;
        setConnected(false);
        es?.close();
        const delay = Math.min(1000 * 2 ** retryRef.current, 10_000);
        retryRef.current++;
        window.setTimeout(connect, delay);
      };
      es.onmessage = (ev) => {
        if (!alive) return;
        try {
          setLast(JSON.parse(ev.data) as T);
        } catch {
          /* ignore */
        }
      };
    };
    connect();
    return () => {
      alive = false;
      setConnected(false);
      es?.close();
    };
  }, [url]);

  return { last, connected };
}

// useEventStream captures every event (not just the latest), up to `limit`.
export function useEventStream<T>(url: string | null, limit = 200): { events: T[]; connected: boolean } {
  const [events, setEvents] = useState<T[]>([]);
  const [connected, setConnected] = useState(false);
  const retryRef = useRef<number>(0);

  useEffect(() => {
    if (!url) return;
    let alive = true;
    let es: EventSource | null = null;

    const connect = () => {
      if (!alive) return;
      es = new EventSource(url);
      es.onopen = () => {
        if (!alive) return;
        retryRef.current = 0;
        setConnected(true);
      };
      es.onerror = () => {
        if (!alive) return;
        setConnected(false);
        es?.close();
        const delay = Math.min(1000 * 2 ** retryRef.current, 10_000);
        retryRef.current++;
        window.setTimeout(connect, delay);
      };
      es.onmessage = (ev) => {
        if (!alive) return;
        try {
          const data = JSON.parse(ev.data) as T;
          setEvents((prev) => {
            const next = [...prev, data];
            if (next.length > limit) next.splice(0, next.length - limit);
            return next;
          });
        } catch {
          /* ignore */
        }
      };
    };
    connect();
    return () => {
      alive = false;
      setConnected(false);
      es?.close();
    };
  }, [url, limit]);

  return { events, connected };
}
