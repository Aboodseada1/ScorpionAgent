/** Strip pseudo-tool / XML junk some models emit into assistant content (safety net with server). */
export function sanitizeAssistantVisibleText(s: string): string {
  if (!s) return "";
  let out = s;
  const cutters = [
    "</function",
    "<function",
    "add_note>",
    "log_qualification>",
    "draft_email>",
    "schedule_call>",
    "</tool",
    "<tool",
  ];
  const lo = out.toLowerCase();
  let best = out.length;
  for (const m of cutters) {
    const i = lo.indexOf(m.toLowerCase());
    if (i >= 0 && i < best) best = i;
  }
  if (best < out.length) out = out.slice(0, best);
  return out.replace(/\s+$/u, "").trim();
}
