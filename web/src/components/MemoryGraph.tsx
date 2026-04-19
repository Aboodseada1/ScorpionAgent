import { useEffect, useMemo, useRef, useState } from "react";

// Tiny dependency-free force-directed graph renderer (SVG). Keeps the bundle
// small — no d3/react-flow. Good enough for up to a few hundred nodes.

interface Node {
  id: string;
  label: string;
  type: string;
  meta?: unknown;
  x?: number;
  y?: number;
  vx?: number;
  vy?: number;
  fx?: number | null;
  fy?: number | null;
}

interface Edge {
  from: string;
  to: string;
  kind: string;
}

const TYPE_COLORS: Record<string, string> = {
  client: "#2F6FED",
  conversation: "#1FA971",
  day: "#E8A33C",
  fact: "#8b867a",
  action: "#D64545",
  turn: "#b9b5a9",
};

export default function MemoryGraph({ nodes, edges }: { nodes: Node[]; edges: Edge[] }) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [selected, setSelected] = useState<Node | null>(null);

  const view = useMemo(() => seedPositions(nodes), [nodes.map((n) => n.id).join(",")]);

  useEffect(() => {
    let raf = 0;
    const tick = () => {
      step(view, edges);
      if (svgRef.current) {
        const g = svgRef.current;
        g.querySelectorAll("[data-node]").forEach((el) => {
          const id = el.getAttribute("data-node")!;
          const n = view.find((x) => x.id === id);
          if (!n) return;
          el.setAttribute("transform", `translate(${n.x},${n.y})`);
        });
        g.querySelectorAll("[data-edge]").forEach((el) => {
          const [f, t] = el.getAttribute("data-edge")!.split("→");
          const a = view.find((x) => x.id === f);
          const b = view.find((x) => x.id === t);
          if (!a || !b) return;
          el.setAttribute("x1", String(a.x));
          el.setAttribute("y1", String(a.y));
          el.setAttribute("x2", String(b.x));
          el.setAttribute("y2", String(b.y));
        });
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [view, edges]);

  const W = 900;
  const H = 500;

  if (nodes.length === 0) {
    return <div className="h-full grid place-items-center text-ink-400">No memory yet.</div>;
  }

  return (
    <div className="relative w-full h-full">
      <svg ref={svgRef} viewBox={`0 0 ${W} ${H}`} className="w-full h-full">
        <g>
          {edges.map((e, i) => (
            <line
              key={i}
              data-edge={`${e.from}→${e.to}`}
              stroke="#d9d7d0"
              strokeWidth={1}
              strokeOpacity={0.6}
            />
          ))}
        </g>
        <g>
          {view.map((n) => (
            <g key={n.id} data-node={n.id} onClick={() => setSelected(n)} style={{ cursor: "pointer" }}>
              <circle r={radius(n.type)} fill={TYPE_COLORS[n.type] || "#5f5c54"} opacity={0.9} />
              <text y={radius(n.type) + 12} textAnchor="middle" fontSize={10} fill="#3e3c36" style={{ pointerEvents: "none" }}>
                {trim(n.label, 32)}
              </text>
            </g>
          ))}
        </g>
      </svg>
      {selected && (
        <div className="absolute top-3 right-3 w-72 bg-card border border-ink-100 rounded-2xl shadow-soft p-3 text-xs">
          <div className="font-medium text-ink-800 mb-1">{selected.label}</div>
          <div className="text-ink-400 capitalize">{selected.type}</div>
          {selected.meta !== undefined && selected.meta !== null && (
            <pre className="mt-2 text-ink-500 whitespace-pre-wrap max-h-60 overflow-y-auto font-mono">
              {JSON.stringify(selected.meta, null, 2)}
            </pre>
          )}
          <button className="mt-2 text-ink-500 hover:text-ink-800" onClick={() => setSelected(null)}>Close</button>
        </div>
      )}
    </div>
  );
}

function radius(type: string): number {
  return type === "client" ? 16 : type === "conversation" ? 12 : type === "day" ? 9 : 7;
}

function trim(s: string, n: number) { return s.length > n ? s.slice(0, n) + "…" : s; }

function seedPositions(nodes: Node[]): Node[] {
  return nodes.map((n, i) => {
    const angle = (i / Math.max(1, nodes.length)) * Math.PI * 2;
    const r = 180 + ((i * 37) % 60);
    return {
      ...n,
      x: 450 + Math.cos(angle) * r,
      y: 250 + Math.sin(angle) * r,
      vx: 0,
      vy: 0,
    };
  });
}

function step(nodes: Node[], edges: Edge[]) {
  const byId: Record<string, Node> = {};
  for (const n of nodes) byId[n.id] = n;
  // Repulsion
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      const a = nodes[i], b = nodes[j];
      const dx = (b.x! - a.x!) || 0.01;
      const dy = (b.y! - a.y!) || 0.01;
      const d2 = dx * dx + dy * dy;
      const k = 1400 / d2;
      const ux = dx / Math.sqrt(d2), uy = dy / Math.sqrt(d2);
      a.vx! -= ux * k;
      a.vy! -= uy * k;
      b.vx! += ux * k;
      b.vy! += uy * k;
    }
  }
  // Springs
  for (const e of edges) {
    const a = byId[e.from];
    const b = byId[e.to];
    if (!a || !b) continue;
    const dx = b.x! - a.x!;
    const dy = b.y! - a.y!;
    const d = Math.sqrt(dx * dx + dy * dy) || 0.01;
    const target = 80;
    const force = (d - target) * 0.02;
    const ux = dx / d, uy = dy / d;
    a.vx! += ux * force;
    a.vy! += uy * force;
    b.vx! -= ux * force;
    b.vy! -= uy * force;
  }
  // Center pull + damping + integrate
  for (const n of nodes) {
    n.vx! += (450 - n.x!) * 0.002;
    n.vy! += (250 - n.y!) * 0.002;
    n.vx! *= 0.8;
    n.vy! *= 0.8;
    n.x! += n.vx!;
    n.y! += n.vy!;
    if (n.x! < 20) n.x = 20;
    if (n.x! > 880) n.x = 880;
    if (n.y! < 20) n.y = 20;
    if (n.y! > 480) n.y = 480;
  }
}
