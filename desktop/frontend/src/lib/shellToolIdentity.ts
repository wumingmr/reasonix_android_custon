/** Exact built-in aliases shared by live events, history and renderers. */
export function isShellToolName(name: string): boolean {
  return /^(bash|pwsh|powershell|shell)$/i.test(name.trim());
}

export function isPowerShellToolName(name: string): boolean {
  return /^(pwsh|powershell)$/i.test(name.trim());
}
