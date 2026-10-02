// A generation fence also rejects callbacks already queued when cancelled.
export class StartupDelay {
  private revision = 0;
  private pending: (() => void) | undefined;
  constructor(private readonly schedule: (callback: () => void) => () => void = (callback) => {
    const timer = setTimeout(callback, 1000);
    timer.unref();
    return () => clearTimeout(timer);
  }) {}

  start(show: () => void): void {
    this.cancel();
    const revision = this.revision;
    this.pending = this.schedule(() => {
      if (revision !== this.revision) return;
      this.cancel();
      show();
    });
  }

  cancel(): void {
    this.revision++;
    this.pending?.();
    this.pending = undefined;
  }
}

export function renderStartupPage(): string {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
<title>Reasonix</title><style>
html,body{height:100%;margin:0;background:#1a1a2e;color:#f4f4f3;font:14px system-ui}
main{height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center}
b{font-size:32px;color:#e58a3a}p{color:#b8b8c8}
</style></head><body><main><b>Reasonix</b><p>Starting… / 正在启动…</p></main></body></html>`;
}
