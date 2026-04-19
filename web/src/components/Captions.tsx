import { useEffect, useRef } from "react";
import clsx from "clsx";

export interface CaptionEntry {
  speaker: string;
  text: string;
  t: number;
  partial?: boolean;
}

export default function Captions({ entries }: { entries: CaptionEntry[] }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [entries.length, entries[entries.length - 1]?.text]);

  return (
    <div ref={ref} className="rounded-3xl bg-card border border-ink-100 shadow-soft p-5 h-[260px] overflow-y-auto">
      {entries.length === 0 && (
        <div className="h-full grid place-items-center text-ink-300 text-sm">Say hello to start the conversation.</div>
      )}
      <ul className="space-y-3">
        {entries.map((e, i) => (
          <li key={i} className={clsx("flex gap-3", e.speaker === "user" && "justify-end")}>
            {e.speaker !== "user" && <SpeakerBadge speaker={e.speaker} />}
            <div
              className={clsx(
                "max-w-[85%] rounded-2xl px-4 py-2 text-sm",
                e.speaker === "user" ? "bg-accent text-white" :
                e.speaker === "assistant" ? "bg-ink-50 text-ink-800" :
                "bg-warn/10 text-warn"
              )}
            >
              {e.text}
              {e.partial && <span className="opacity-40 animate-pulse">▍</span>}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function SpeakerBadge({ speaker }: { speaker: string }) {
  return (
    <span className={clsx(
      "h-7 w-7 shrink-0 rounded-full grid place-items-center text-xs text-white",
      speaker === "assistant" ? "bg-ink-700" : "bg-warn"
    )}>
      {speaker[0]?.toUpperCase() || "?"}
    </span>
  );
}
