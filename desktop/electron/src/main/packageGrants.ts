import { execFileSync } from "node:child_process";

const SHOWN_PATHS = 5;

export interface GrantReport {
  stripped: string[];
  refused: string[];
}

export interface GrantTarget {
  platform: NodeJS.Platform;
  packaged: boolean;
  execPath: string;
}

type Run = (file: string, args: string[], options: { encoding: "utf8"; timeout: number; windowsHide: boolean; stdio: ["ignore", "pipe", "pipe"] }) => string;

// Chromium's sandboxed children exit while loading a DLL whose access entries
// grant one Windows app package, so the window never paints. The service
// removes those grants from the tree this executable runs from, and it has to
// happen before Chromium starts its first child, which is why this is sync.
export function stripPackageGrants(binary: string, target: GrantTarget, log: (line: string) => void, run: Run = execFileSync as unknown as Run): GrantReport | null {
  if (target.platform !== "win32" || !target.packaged) return null;
  try {
    const raw = run(binary, ["-strip-package-grants", "-app", target.execPath], {
      encoding: "utf8",
      timeout: 15000,
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    return readGrantReport(raw);
  } catch (error) {
    const detail = error as { stderr?: unknown; message?: unknown };
    log(`strip-package-grants: ${String(detail.stderr ?? "").trim() || String(detail.message ?? error)}`);
    return null;
  }
}

export function readGrantReport(raw: string): GrantReport {
  const body = JSON.parse(raw) as { stripped?: unknown; refused?: unknown };
  if (!Array.isArray(body?.stripped) || !Array.isArray(body?.refused)) {
    throw new Error("the grant report carried no lists");
  }
  const stripped = body.stripped.filter((p): p is string => typeof p === "string");
  const refused = body.refused.map((r) => (r as { path?: unknown })?.path).filter((p): p is string => typeof p === "string");
  return { stripped, refused };
}

// What to tell someone whose window died before it painted, when the service
// already knows why: grants it found and could not remove. Any other cause is
// one this shell does not know, and it says nothing rather than guess.
export function unpaintedWindowCause(report: GrantReport | null, locale: string): { title: string; detail: string } | null {
  if (!report?.refused.length) return null;
  const shown = report.refused.slice(0, SHOWN_PATHS).join("\n");
  const more = report.refused.length - SHOWN_PATHS;
  if (locale.toLowerCase().startsWith("zh")) {
    return {
      title: "Reasonix 无法打开窗口",
      detail:
        "Reasonix 加载的文件上有授予某个 Windows 应用包（AppContainer）的访问项，Chromium 的沙箱进程加载这类文件时会退出。" +
        "Reasonix 尝试移除它们，以下位置被拒绝：\n\n" + shown + (more > 0 ? `\n……另有 ${more} 处` : "") +
        "\n\n以管理员身份运行一次 Reasonix，它会自行移除这些访问项。",
    };
  }
  return {
    title: "Reasonix could not open its window",
    detail:
      "Files Reasonix loads carry access entries granting a specific Windows app package (AppContainer), " +
      "and Chromium's sandboxed processes exit while loading such a file. Reasonix tried to remove them and was refused for:\n\n" +
      shown + (more > 0 ? `\n…and ${more} more` : "") +
      "\n\nRun Reasonix once as administrator and it will remove them itself.",
  };
}
