// Presentation only: never rewrite persisted or provider-visible messages.
// Keep this tag vocabulary aligned with agent.TransientUserBlockTags.
export const transientUserTags = [
  "response-language", "reasoning-language", "memory-update", "background-jobs",
  "active-goal", "autoresearch-runtime", "hook-context", "capability-route",
  "interrupted-turn-recovery", "execution-policy",
] as const;
const leadingBlock = new RegExp(`^\\s*<(${transientUserTags.join("|")})(?:\\s+[^>]*)?>[\\s\\S]*?</\\1>\\s*`);
const steerPrefix = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]";
const deliveryMarker = `<delivery-runtime>
This session is in delivery-first mode. Use todo_write when a task benefits from
an explicit task list, update it from your own assessment, and finish when the
user's request is handled. Structured file tools require a current host-observed
file version; reading any useful window establishes that observation.
</delivery-runtime>`;
const hostRecoveryPrefixes = [
  "A tool failed. Use read-only diagnosis as needed",
  "The tool timed out or hit a transient execution limit.",
];

function legacyText(content: string): string {
  let text = content;
  for (let depth = 0; depth < 24; depth++) {
    const next = text.replace(/<memory-compiler-execution>\s*([\s\S]*?)\s*<\/memory-compiler-execution>/g, (_block, json: string) => {
      try {
        const contract = JSON.parse(json);
        const source = contract?.planner_ir?.source_event || contract?.source_event;
        return typeof source === "string" ? source : "";
      } catch { return ""; }
    }).replace(leadingBlock, "");
    if (next === text) break;
    text = next;
  }
  if (text.trimStart().startsWith("# Reasonix executor handoff")) {
    const start = text.indexOf("Original task:\n");
    if (start >= 0) text = text.slice(start + "Original task:\n".length).split("\n\nPlanner output:")[0];
    while (leadingBlock.test(text)) text = text.replace(leadingBlock, "");
  }
  text = text.trimEnd();
  if (text.endsWith(deliveryMarker)) text = text.slice(0, -deliveryMarker.length);
  text = text.replace(/\n*<execution-policy(?:\s+[^>]*)?>[\s\S]*?<\/execution-policy>\s*$/, "");
  if (text.trimEnd().endsWith("</memory-recall>")) {
    const index = text.lastIndexOf("<memory-recall>");
    if (index >= 0) text = text.slice(0, index);
  }
  return text.trim();
}

export function canonicalSteerGuidance(content: string): string | undefined {
  let framed = content;
  for (let depth = 0; depth < 24; depth++) {
    if (framed.startsWith(steerPrefix)) {
      let text = framed.slice(steerPrefix.length);
      if (text.startsWith("\n")) text = text.slice(1);
      if (text.endsWith(`\n\n${deliveryMarker}`)) text = text.slice(0, -(`\n\n${deliveryMarker}`).length);
      return text;
    }
    const trimmed = framed.replace(/^[ \t\r\n]+/, "");
    const tag = ["response-language", "reasoning-language"].find(value => trimmed.startsWith(`<${value}>`) || trimmed.startsWith(`<${value} `));
    if (!tag) return undefined;
    const end = trimmed.indexOf(`</${tag}>`);
    if (end < 0) return undefined;
    framed = trimmed.slice(end + tag.length + 3).replace(/^[ \t\r\n]+/, "");
  }
  return undefined;
}

export function canonicalUserDisplay(raw: Record<string, unknown>, fallback: string): { role: string; content: string } {
  // An explicit origin wins over text heuristics. A user's quoted XML stays
  // visible, while host snapshots stay hidden even when they have RawContent.
  if (raw.origin === "host") return { role: "hidden", content: "" };
  const stored = typeof raw.content === "string" ? raw.content : fallback;
  const authored = typeof raw.raw_content === "string" && raw.raw_content.trim() ? raw.raw_content : undefined;
  const steer = legacyText(stored);
  const text = authored ?? steer;
  if (raw.origin !== "user" && !authored && /^<session-context version="1">\s*This host-generated snapshot supersedes every earlier session-context snapshot\./.test(text)) {
    return { role: "hidden", content: "" };
  }
  if (steer.startsWith(steerPrefix)) {
    const content = authored ?? steer.slice(steerPrefix.length).trim();
    if (raw.origin !== "user" && !authored && hostRecoveryPrefixes.some(prefix => content.startsWith(prefix))) return { role: "hidden", content: "" };
    return { role: "notice", content: `↪ ${content}` };
  }
  if (raw.origin !== "user" && !authored && hostRecoveryPrefixes.some(prefix => text.startsWith(prefix))) return { role: "hidden", content: "" };
  return { role: "user", content: text };
}
