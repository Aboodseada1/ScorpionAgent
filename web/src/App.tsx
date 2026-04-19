import { NavLink, Outlet, useLocation } from "react-router-dom";
import { AnimatePresence, motion } from "framer-motion";
import { Users, Brain, Settings as SettingsIcon, Activity, Circle, LineChart } from "lucide-react";
import { useEffect, useState } from "react";
import { api, type StatusResp } from "./lib/api";
import clsx from "clsx";

const pageEase = [0.22, 1, 0.36, 1] as const;

export default function App() {
  const location = useLocation();
  const [status, setStatus] = useState<StatusResp | null>(null);

  useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const s = await api.status();
        if (alive) setStatus(s);
      } catch {
        if (alive) setStatus(null);
      }
    };
    tick();
    const id = window.setInterval(tick, 6000);
    return () => {
      alive = false;
      window.clearInterval(id);
    };
  }, []);

  return (
    <div className="flex h-full w-full bg-paper text-ink-800">
      <aside className="w-64 shrink-0 border-r border-ink-100 bg-paper flex flex-col">
        <div className="px-6 py-6">
          <div className="flex items-center gap-3">
            <div className="h-10 w-10 rounded-2xl bg-ink-800 text-paper grid place-items-center font-display text-lg">S</div>
            <div>
              <div className="font-display text-lg leading-tight">Scorpion</div>
              <div className="text-xs text-ink-400">SDR Agent</div>
            </div>
          </div>
        </div>
        <nav className="px-3 flex flex-col gap-1">
          <NavItem to="/clients" icon={<Users size={18} />} label="Clients" />
          <NavItem to="/memory" icon={<Brain size={18} />} label="Memory" />
          <NavItem to="/monitor" icon={<LineChart size={18} />} label="Monitor" />
          <NavItem to="/settings" icon={<SettingsIcon size={18} />} label="Settings" />
        </nav>
        <div className="mt-auto p-4">
          <StatusCard status={status} />
        </div>
      </aside>
      <main className="flex-1 h-full overflow-y-auto relative">
        <AnimatePresence mode="wait" initial={false}>
          <motion.div
            key={location.pathname}
            className="min-h-full"
            initial={{ opacity: 0, y: 12 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -8 }}
            transition={{ duration: 0.28, ease: pageEase }}
          >
            <Outlet />
          </motion.div>
        </AnimatePresence>
      </main>
    </div>
  );
}

function NavItem({ to, icon, label }: { to: string; icon: React.ReactNode; label: string }) {
  return (
    <NavLink to={to} className="block rounded-xl">
      {({ isActive }) => (
        <motion.span
          className={clsx(
            "flex items-center gap-3 px-3 py-2 rounded-xl text-sm transition-colors",
            isActive ? "bg-ink-800 text-paper shadow-soft" : "text-ink-600 hover:bg-ink-50",
          )}
          layout
          transition={{ type: "spring", stiffness: 420, damping: 32 }}
          whileHover={{ x: 2 }}
          whileTap={{ scale: 0.98 }}
        >
          {icon}
          <span>{label}</span>
        </motion.span>
      )}
    </NavLink>
  );
}

function StatusCard({ status }: { status: StatusResp | null }) {
  if (!status) {
    return (
      <motion.div
        initial={{ opacity: 0.6 }}
        animate={{ opacity: 1 }}
        className="rounded-2xl border border-ink-100 bg-paperDim p-3 text-xs text-ink-400"
      >
        <Activity size={14} className="inline mr-2" />
        connecting…
      </motion.div>
    );
  }
  const all = status.llm.ok && status.stt.ok && status.piper.ok;
  return (
    <motion.div
      layout
      initial={{ opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25, ease: pageEase }}
      className="rounded-2xl border border-ink-100 bg-card p-3 text-xs space-y-2 shadow-soft"
    >
      <div className={clsx("flex items-center gap-2 font-medium", all ? "text-ok" : "text-warn")}>
        <Circle size={10} className="fill-current" />
        {all ? "All systems ready" : "Partially ready"}
      </div>
      <Dot label="LLM" ok={status.llm.ok} sub={status.llm.model} />
      <Dot label="STT" ok={status.stt.ok} sub={status.stt.model} />
      <Dot label="TTS" ok={status.piper.ok} sub={status.piper.model ? "piper" : "unset"} />
    </motion.div>
  );
}

function Dot({ label, ok, sub }: { label: string; ok: boolean; sub: string }) {
  return (
    <div className="flex items-center justify-between text-ink-500">
      <div className="flex items-center gap-2">
        <span className={clsx("h-2 w-2 rounded-full", ok ? "bg-ok" : "bg-bad")} />
        <span>{label}</span>
      </div>
      <span className="text-ink-300 truncate max-w-[100px]" title={sub}>
        {sub}
      </span>
    </div>
  );
}
