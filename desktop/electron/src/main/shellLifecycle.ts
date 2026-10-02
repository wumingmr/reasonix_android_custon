import { randomUUID } from "node:crypto";
import { mkdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { HelloResult } from "./handshake.js";

type ShellBuild = { version: string; channel: string; commit: string };

export class ShellLifecycle {
  private path = "";
  private state: Record<string, unknown> | null = null;

  constructor(
    private readonly dataHome: string,
    private readonly build: ShellBuild,
    private readonly now = () => new Date(),
  ) {}

  start(hello: HelloResult): void {
    if (!hello.diagnosticsEnabled || this.path) return;
    const runId = randomUUID().replaceAll("-", "");
    const at = this.now().toISOString();
    this.path = join(this.dataHome, "diagnostics", "lifecycle", `shell-${process.pid}-${runId}.json`);
    this.state = {
      schemaVersion: 3,
      pid: process.pid,
      runId,
      incidentId: hello.incidentId,
      version: this.build.version,
      buildCommit: this.build.commit,
      channel: this.build.channel,
      processRole: "shell",
      phase: "healthy",
      startedAt: at,
      updatedAt: at,
    };
    this.write();
  }

  mark(phase: string): void {
    if (!this.state || !phase) return;
    this.state.phase = phase;
    this.state.updatedAt = this.now().toISOString();
    this.write();
  }

  complete(): void {
    if (!this.path) return;
    rmSync(this.path, { force: true });
    this.path = "";
    this.state = null;
  }

  private write(): void {
    if (!this.path || !this.state) return;
    mkdirSync(dirname(this.path), { recursive: true, mode: 0o700 });
    const temporary = `${this.path}.tmp-${process.pid}`;
    writeFileSync(temporary, `${JSON.stringify(this.state)}\n`, {
      mode: 0o600,
    });
    renameSync(temporary, this.path);
  }
}
