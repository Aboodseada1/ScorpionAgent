import { useEffect, useRef, useState } from "react";
import {
  Activity,
  Cpu,
  HardDrive,
  MemoryStick,
  Network,
  Server,
  Zap,
  Clock,
  Circle,
} from "lucide-react";
import clsx from "clsx";
import { useEventSource } from "../lib/sse";
import { api, type Metrics } from "../lib/api";

const HISTORY_LENGTH = 60;

export default function MonitorPage() {
  const [snapshot, setSnapshot] = useState<Metrics | null>(null);
  const { last, connected } = useEventSource<Metrics>("/api/metrics/stream");
  const [cpuHist, setCpuHist] = useState<number[]>([]);
  const [memHist, setMemHist] = useState<number[]>([]);
  const [netRxHist, setNetRxHist] = useState<number[]>([]);
  const [netTxHist, setNetTxHist] = useState<number[]>([]);
  const seenTs = useRef<number>(0);

  useEffect(() => {
    api.metrics().then(setSnapshot).catch(() => {});
  }, []);

  useEffect(() => {
    if (!last) return;
    if (last.ts === seenTs.current) return;
    seenTs.current = last.ts;
    setSnapshot(last);
    setCpuHist((p) => trim([...p, last.cpu.used_percent]));
    setMemHist((p) => trim([...p, last.memory.used_percent]));
    setNetRxHist((p) => trim([...p, last.net.rx_kbps]));
    setNetTxHist((p) => trim([...p, last.net.tx_kbps]));
  }, [last]);

  return (
    <div className="h-full">
      <div className="px-10 pt-10 pb-4 flex items-start justify-between">
        <div>
          <div className="text-xs uppercase tracking-wider text-ink-400">System</div>
          <h1 className="font-display text-4xl text-ink-900 mt-1">Monitor</h1>
          <div className="text-sm text-ink-500 mt-2">
            {snapshot ? (
              <>
                <span className="font-mono">{snapshot.hostname}</span>
                <span className="text-ink-300 mx-2">•</span>
                <span>Linux {snapshot.kernel}</span>
                <span className="text-ink-300 mx-2">•</span>
                <span>Uptime {humanUptime(snapshot.uptime_sec)}</span>
              </>
            ) : (
              <span className="text-ink-300">collecting…</span>
            )}
          </div>
        </div>
        <div
          className={clsx(
            "flex items-center gap-2 text-xs font-mono px-3 py-1.5 rounded-full",
            connected ? "bg-ok/10 text-ok" : "bg-warn/10 text-warn",
          )}
        >
          <Circle size={8} className="fill-current" />
          {connected ? "live" : "reconnecting"}
        </div>
      </div>

      <div className="px-10 pb-16 space-y-6">
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-5">
          <Gauge
            icon={<Cpu size={18} />}
            title="CPU"
            value={snapshot?.cpu.used_percent ?? 0}
            suffix="%"
            footer={
              snapshot
                ? `${snapshot.cpu.cores} cores · load ${snapshot.cpu.load1.toFixed(2)} / ${snapshot.cpu.load5.toFixed(2)} / ${snapshot.cpu.load15.toFixed(2)}`
                : "—"
            }
            history={cpuHist}
            max={100}
            color="accent"
          />
          <Gauge
            icon={<MemoryStick size={18} />}
            title="Memory"
            value={snapshot?.memory.used_percent ?? 0}
            suffix="%"
            footer={
              snapshot
                ? `${humanMB(snapshot.memory.used_mb)} / ${humanMB(snapshot.memory.total_mb)}`
                : "—"
            }
            history={memHist}
            max={100}
            color="warn"
          />
          <Gauge
            icon={<HardDrive size={18} />}
            title="Disk"
            value={snapshot?.disk.used_percent ?? 0}
            suffix="%"
            footer={
              snapshot
                ? `${snapshot.disk.used_gb.toFixed(1)} / ${snapshot.disk.total_gb.toFixed(0)} GB on ${snapshot.disk.mount}`
                : "—"
            }
            history={[]}
            max={100}
            color="ink"
          />
          <Gauge
            icon={<Network size={18} />}
            title="Network"
            value={(snapshot?.net.rx_kbps ?? 0) + (snapshot?.net.tx_kbps ?? 0)}
            suffix=" KB/s"
            footer={
              snapshot
                ? `${snapshot.net.iface} · ↓${fmtKBps(snapshot.net.rx_kbps)}  ↑${fmtKBps(snapshot.net.tx_kbps)}`
                : "—"
            }
            history={netRxHist.map((v, i) => v + (netTxHist[i] ?? 0))}
            max={Math.max(200, Math.max(...netRxHist, ...netTxHist) * 1.2 || 200)}
            color="ok"
            numeric
          />
        </div>

        {snapshot?.gpu && (
          <Card title="GPU" icon={<Zap size={16} />}>
            <div className="grid grid-cols-4 gap-4 text-sm">
              <Stat label="Device" value={snapshot.gpu.name} mono />
              <Stat label="Utilization" value={`${snapshot.gpu.util_percent.toFixed(0)}%`} />
              <Stat
                label="VRAM"
                value={`${(snapshot.gpu.mem_used_mb / 1024).toFixed(1)} / ${(snapshot.gpu.mem_total_mb / 1024).toFixed(1)} GB`}
              />
              <Stat label="Temp" value={`${snapshot.gpu.temperature_c.toFixed(0)} °C`} />
            </div>
          </Card>
        )}

        <Card title="Services" icon={<Server size={16} />}>
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
            {(snapshot?.services ?? []).map((s) => (
              <ServiceCard key={s.name} s={s} />
            ))}
            {!snapshot && (
              <div className="col-span-3 text-ink-400 text-sm">Loading services…</div>
            )}
          </div>
        </Card>

        <Card title="Memory & swap" icon={<Activity size={16} />}>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <Stat label="Total" value={humanMB(snapshot?.memory.total_mb ?? 0)} />
            <Stat label="Used" value={humanMB(snapshot?.memory.used_mb ?? 0)} />
            <Stat label="Available" value={humanMB(snapshot?.memory.available_mb ?? 0)} />
            <Stat
              label="Swap"
              value={`${humanMB(snapshot?.memory.swap_used_mb ?? 0)} / ${humanMB(snapshot?.memory.swap_total_mb ?? 0)}`}
            />
          </div>
        </Card>

        <Card title="Network" icon={<Network size={16} />}>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            <Stat label="Interface" value={snapshot?.net.iface ?? "—"} mono />
            <Stat label="Download" value={fmtKBps(snapshot?.net.rx_kbps ?? 0)} />
            <Stat label="Upload" value={fmtKBps(snapshot?.net.tx_kbps ?? 0)} />
            <Stat
              label="Lifetime"
              value={`↓ ${(snapshot?.net.rx_total_gb ?? 0).toFixed(2)} GB  ↑ ${(snapshot?.net.tx_total_gb ?? 0).toFixed(2)} GB`}
            />
          </div>
        </Card>
      </div>
    </div>
  );
}

function Gauge({
  icon,
  title,
  value,
  suffix = "",
  footer,
  history,
  max,
  color,
  numeric = false,
}: {
  icon: React.ReactNode;
  title: string;
  value: number;
  suffix?: string;
  footer: string;
  history: number[];
  max: number;
  color: "accent" | "warn" | "ok" | "ink";
  numeric?: boolean;
}) {
  const colors = {
    accent: { stroke: "#0D6EFD", fill: "rgba(13, 110, 253, 0.12)", text: "text-accent" },
    warn: { stroke: "#F59E0B", fill: "rgba(245, 158, 11, 0.12)", text: "text-warn" },
    ok: { stroke: "#10B981", fill: "rgba(16, 185, 129, 0.12)", text: "text-ok" },
    ink: { stroke: "#1F2937", fill: "rgba(31, 41, 55, 0.10)", text: "text-ink-700" },
  }[color];
  const displayValue = numeric ? value.toFixed(1) : value.toFixed(0);
  return (
    <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-5 relative overflow-hidden">
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-ink-400">
          <span className={colors.text}>{icon}</span>
          {title}
        </div>
      </div>
      <div className="mt-3 font-display text-4xl text-ink-800 tabular-nums">
        {displayValue}
        <span className="text-ink-400 text-2xl ml-1">{suffix}</span>
      </div>
      <div className="text-xs text-ink-500 mt-1">{footer}</div>
      <div className="mt-4 h-12">
        <Sparkline data={history} max={max} stroke={colors.stroke} fill={colors.fill} />
      </div>
    </div>
  );
}

function Sparkline({
  data,
  max,
  stroke,
  fill,
}: {
  data: number[];
  max: number;
  stroke: string;
  fill: string;
}) {
  const w = 240;
  const h = 48;
  if (data.length < 2) {
    return (
      <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-full">
        <line x1={0} y1={h - 1} x2={w} y2={h - 1} stroke="#eceae4" strokeWidth={1} />
      </svg>
    );
  }
  const points = data.map((v, i) => {
    const x = (i / (HISTORY_LENGTH - 1)) * w;
    const y = h - Math.max(0, Math.min(v / max, 1)) * h;
    return [x, y] as const;
  });
  const d = points.map(([x, y], i) => (i === 0 ? `M${x},${y}` : `L${x},${y}`)).join(" ");
  const fillPath = `${d} L${points[points.length - 1][0]},${h} L0,${h} Z`;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-full" preserveAspectRatio="none">
      <path d={fillPath} fill={fill} />
      <path d={d} stroke={stroke} strokeWidth={1.5} fill="none" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

function Card({ title, icon, children }: { title: string; icon?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="rounded-3xl bg-card border border-ink-100 shadow-soft p-6">
      <div className="flex items-center gap-2 font-display text-xl mb-4">
        {icon}
        {title}
      </div>
      {children}
    </div>
  );
}

function Stat({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="text-xs uppercase tracking-wider text-ink-400">{label}</div>
      <div className={clsx("text-ink-800 mt-1 text-base", mono && "font-mono")}>{value}</div>
    </div>
  );
}

function ServiceCard({ s }: { s: Metrics["services"][number] }) {
  const ok = s.active === "active" && s.sub_state === "running";
  const status = s.active === "active" ? s.sub_state : s.active;
  return (
    <div className="rounded-2xl border border-ink-100 bg-paper p-4">
      <div className="flex items-center justify-between">
        <div className="font-mono text-sm text-ink-800 truncate" title={s.name}>
          {displayUnit(s.name)}
        </div>
        <span
          className={clsx(
            "px-2 py-0.5 rounded-full text-[10px] uppercase tracking-wider",
            ok ? "bg-ok/10 text-ok" : "bg-bad/10 text-bad",
          )}
        >
          {status || "unknown"}
        </span>
      </div>
      <div className="text-xs text-ink-500 mt-1 line-clamp-2">{s.description || "\u00A0"}</div>
      <div className="grid grid-cols-3 gap-2 mt-3 text-xs font-mono">
        <MetricLine icon={<Activity size={10} />} label="cpu" value={`${s.cpu_percent.toFixed(1)}%`} />
        <MetricLine icon={<MemoryStick size={10} />} label="mem" value={humanMB(s.memory_mb)} />
        <MetricLine icon={<Clock size={10} />} label="up" value={humanUptime(s.uptime_sec)} />
      </div>
      <div className="text-[10px] text-ink-300 mt-2 font-mono">PID {s.pid || "—"}</div>
    </div>
  );
}

function MetricLine({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <div className="flex items-center gap-1 text-ink-500">
      <span className="text-ink-400">{icon}</span>
      <span className="uppercase tracking-wider">{label}</span>
      <span className="ml-auto text-ink-700">{value}</span>
    </div>
  );
}

function humanMB(mb: number): string {
  if (!mb || mb < 0) return "0 MB";
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`;
  return `${mb.toFixed(0)} MB`;
}

function humanUptime(s: number): string {
  if (!s || s <= 0) return "—";
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function fmtKBps(v: number): string {
  if (!isFinite(v) || v <= 0) return "0 KB/s";
  if (v >= 1024) return `${(v / 1024).toFixed(1)} MB/s`;
  return `${v.toFixed(1)} KB/s`;
}

function displayUnit(name: string): string {
  return name.replace(/\.service$/, "");
}

function trim(arr: number[]): number[] {
  if (arr.length <= HISTORY_LENGTH) return arr;
  return arr.slice(arr.length - HISTORY_LENGTH);
}
