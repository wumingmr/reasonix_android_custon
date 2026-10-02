import { spawn as nodeSpawn, type ChildProcess, type SpawnOptions } from "node:child_process";
import { randomUUID } from "node:crypto";
import type { EventFrame, ServiceState } from "../shared/ipc.js";
import { eventFrame } from "../shared/eventStream.js";
import type { HelloResult } from "./handshake.js";
import { errorText, type Logger } from "./log.js";
import { RestartBudget } from "./restartBudget.js";
import { RpcClient } from "./rpc.js";

export const LIFECYCLE_TIMEOUT_MS = 10_000;
export const EXIT_GRACE_MS = 5_000;

export interface ShutdownTiming {
  replyMs: number; // silence on desktop/shutdown before its status is followed
  pollMs: number;
  stallMs: number; // longest a live shutdown may keep one phase and outcome
  ceilingMs: number; // hard bound on the whole wait, however it advances
}

export const SHUTDOWN_TIMING: ShutdownTiming = {
  replyMs: LIFECYCLE_TIMEOUT_MS,
  pollMs: 250,
  stallMs: 30_000,
  ceilingMs: 60_000,
};

// code is the failure's identity; the message is only its display form.
export class ShutdownError extends Error {
  constructor(
    readonly code: string,
    detail: string,
  ) {
    super(`${code}: ${detail}`);
    this.name = "ShutdownError";
  }
}

export interface ServiceHandlers {
  hello(client: RpcClient): Promise<HelloResult>;
  onRequest(method: string, params: unknown): Promise<unknown>;
  onEvent(frame: EventFrame): void;
  onState(state: ServiceState): void;
  onReady(hello: HelloResult, restarted: boolean): void | Promise<void>;
  onFailed(error: unknown): void;
}

export type SpawnFn = (command: string, args: string[], options: SpawnOptions) => ChildProcess;

export interface ServiceOptions {
  binary: string;
  args: string[];
  env: NodeJS.ProcessEnv;
  onStderr(chunk: Buffer): void;
  log: Logger;
  spawn?: SpawnFn;
  budget?: RestartBudget;
  now?: () => number;
  exitGraceMs?: number;
  shutdownTiming?: Partial<ShutdownTiming>;
}

interface Session {
  child: ChildProcess;
  client: RpcClient;
  generation: string;
  alive: boolean;
  ready: boolean;
  eventSeq: number;
  expectExit: boolean;
  exited: Promise<void>;
}

type ShutdownResult = {
  requestId: string;
  reason: string;
  phase: "idle" | "preparing" | "saving" | "closing" | "completed";
  outcome: "not_started" | "in_progress" | "success" | "failed";
  completed: boolean;
  retryable: boolean;
  errorCode?: string;
  error?: string;
};

export type ShutdownPhase = ShutdownResult["phase"];

function shutdownResult(value: unknown): ShutdownResult {
  if (!value || typeof value !== "object") throw new Error("desktop shutdown returned an invalid result");
  const result = value as Partial<ShutdownResult>;
  if (
    typeof result.requestId !== "string" ||
    typeof result.phase !== "string" ||
    typeof result.outcome !== "string" ||
    typeof result.completed !== "boolean" ||
    typeof result.retryable !== "boolean"
  ) {
    throw new Error("desktop shutdown returned an invalid result");
  }
  return result as ShutdownResult;
}

function describeExit(code: number | null, signal: NodeJS.Signals | null): string {
  return signal ? `signal ${signal}` : `code ${code ?? "unknown"}`;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function within<T>(promise: Promise<T>, ms: number): Promise<T | null> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([promise, new Promise<null>((resolve) => (timer = setTimeout(() => resolve(null), ms)))]);
  } finally {
    clearTimeout(timer);
  }
}

export class ServiceSupervisor {
  private session: Session | null = null;
  private state: ServiceState = { phase: "starting", generation: "" };
  private hello: HelloResult | null = null;
  private launching: Promise<HelloResult> | null = null;
  private stopping = false;
  private shutdownPending: Promise<void> | null = null;
  private shutdownRequestId = "";
  private revision = 0;
  private restarting: Promise<HelloResult> | null = null;
  private readonly budget: RestartBudget;
  private readonly spawnFn: SpawnFn;
  private readonly now: () => number;
  private readonly exitGraceMs: number;
  private readonly shutdownTiming: ShutdownTiming;

  constructor(
    private readonly options: ServiceOptions,
    private readonly handlers: ServiceHandlers,
  ) {
    this.budget = options.budget ?? new RestartBudget();
    this.spawnFn = options.spawn ?? ((command, args, spawnOptions) => nodeSpawn(command, args, spawnOptions));
    this.now = options.now ?? (() => Date.now());
    this.exitGraceMs = options.exitGraceMs ?? EXIT_GRACE_MS;
    this.shutdownTiming = { ...SHUTDOWN_TIMING, ...options.shutdownTiming };
  }

  get current(): ServiceState {
    return this.state;
  }

  get helloResult(): HelloResult | null {
    return this.hello;
  }

  get generation(): string {
    return this.session?.alive && this.session.ready ? this.session.generation : "";
  }

  get ready(): boolean {
    return !this.stopping && this.state.phase === "ready" && this.session?.alive === true;
  }

  get shutdownRequestIdentity(): string {
    return this.shutdownRequestId;
  }

  start(): Promise<HelloResult> {
    return this.begin(false);
  }

  async restart(): Promise<HelloResult> {
    if (this.stopping) throw new Error("desktop service is shutting down");
    if (this.launching) return this.launching;
    if (!this.restarting) {
      this.restarting = (async () => {
        const old = this.session;
        if (old?.alive) await this.terminate(old);
        return this.begin(true);
      })().finally(() => {
        this.restarting = null;
      });
    }
    return this.restarting;
  }

  async invoke(method: string, args: unknown[]): Promise<unknown> {
    return this.live().client.request("desktop/invoke", { method, args });
  }

  async request(method: string, params: unknown, timeoutMs = LIFECYCLE_TIMEOUT_MS): Promise<unknown> {
    return this.live().client.request(method, params, timeoutMs);
  }

  async hostEvent(name: string, payload: unknown): Promise<void> {
    try {
      await this.request("desktop/hostEvent", { name, payload });
    } catch (error) {
      this.options.log.warn(`hostEvent ${name} not delivered: ${errorText(error)}`);
    }
  }

  shutdown(
    reason: "user_quit" | "update_restart" | "system_signal" = "user_quit",
    onProgress?: (phase: ShutdownPhase) => void,
  ): Promise<void> {
    if (!this.stopping) {
      this.stopping = true;
      this.revision++;
      this.setState({ phase: "stopping", generation: this.session?.generation ?? "" });
    }
    if (!this.shutdownPending) {
      this.shutdownPending = this.finishShutdown(reason, onProgress)
        .catch((error) => {
          if (this.state.phase !== "exited") {
            this.setState({ phase: "stopping", generation: this.session?.generation ?? "", error: errorText(error) });
          }
          throw error;
        })
        .finally(() => {
          this.shutdownPending = null;
        });
    }
    return this.shutdownPending;
  }

  private async finishShutdown(
    reason: "user_quit" | "update_restart" | "system_signal",
    onProgress?: (phase: ShutdownPhase) => void,
  ): Promise<void> {
    // A restart may already own termination of the current generation. Let that
    // operation settle before selecting the session to shut down, otherwise the
    // shutdown RPC races a closing stdin and turns an intentional quit into an
    // indeterminate transport error.
    if (this.restarting) await this.restarting.catch(() => undefined);
    const session = this.session;
    if (!session?.alive) {
      this.setState({ phase: "exited", generation: "" });
      return;
    }
    session.expectExit = true;
    if (!this.shutdownRequestId) this.shutdownRequestId = randomUUID();
    const result = await this.awaitShutdown(session, reason, onProgress);
    if (result.outcome === "failed") {
      throw new ShutdownError(result.errorCode ?? "shutdown_failed", result.error ?? "desktop shutdown failed");
    }
    if (!result.completed || result.outcome !== "success") {
      throw new ShutdownError(result.errorCode ?? "shutdown_incomplete", result.error ?? `desktop shutdown stopped in ${result.phase}`);
    }
    // The service closes itself only after the completed result has reached the
    // shell. stdin/kill are now a post-completion process-exit fallback.
    await this.terminate(session);
    this.setState({ phase: "exited", generation: "" });
  }

  // The shutdown reply stays awaited for the whole wait: a completed result that
  // arrives after replyMs is the service's verdict, not an orphan. Status polls
  // only decide whether the shutdown is still live and advancing meanwhile.
  private async awaitShutdown(
    session: Session,
    reason: "user_quit" | "update_restart" | "system_signal",
    onProgress?: (phase: ShutdownPhase) => void,
  ): Promise<ShutdownResult> {
    const timing = this.shutdownTiming;
    const requestId = this.shutdownRequestId;
    const startedAt = this.now();
    const reply = session.client
      .request("desktop/shutdown", { requestId, reason }, timing.ceilingMs)
      .then((value) => ({ result: shutdownResult(value) }))
      .catch((error: unknown) => ({ error }));
    const first = await within(reply, timing.replyMs);
    const unanswered = new Promise<never>(() => undefined);
    const answer = first ? unanswered : reply.then((settled) => ("result" in settled ? settled.result : unanswered));
    let result = null as ShutdownResult | null;
    let advancedAt = startedAt;
    const observe = (next: ShutdownResult) => {
      if (!result || next.phase !== result.phase || next.outcome !== result.outcome) {
        advancedAt = this.now();
        onProgress?.(next.phase);
      }
      result = next;
    };
    if (first && "result" in first) observe(first.result);
    else {
      const why = first ? errorText(first.error) : `no reply after ${timing.replyMs} ms`;
      this.options.log.warn(`desktop/shutdown result unknown: ${why}; following status`);
    }
    for (;;) {
      if (result && (result.completed || result.outcome !== "in_progress")) return result;
      const phase = result?.phase ?? "unknown";
      if (this.now() - startedAt >= timing.ceilingMs) {
        throw new ShutdownError("shutdown_incomplete", `desktop shutdown did not complete within ${timing.ceilingMs} ms; last phase ${phase}`);
      }
      if (this.now() - advancedAt >= timing.stallMs) {
        throw new ShutdownError("shutdown_incomplete", `desktop shutdown stopped in ${phase}`);
      }
      const late = await within(answer, timing.pollMs);
      if (late) {
        observe(late);
        continue;
      }
      if (!session.alive) {
        throw new ShutdownError("service_exited", `desktop service exited during shutdown in phase ${phase}`);
      }
      try {
        observe(shutdownResult(await session.client.request("desktop/shutdownStatus", { requestId }, timing.replyMs)));
      } catch (error) {
        if (session.alive) this.options.log.warn(`desktop/shutdownStatus unanswered: ${errorText(error)}`);
      }
    }
  }

  private live(): Session {
    const session = this.session;
    if (this.stopping) throw new Error("desktop service is shutting down");
    if (!session?.alive || !session.ready) throw new Error(`desktop service is not running (${this.state.phase})`);
    return session;
  }

  private begin(restarted: boolean): Promise<HelloResult> {
    if (this.stopping) return Promise.reject(new Error("desktop service is shutting down"));
    if (this.launching) return this.launching;
    this.launching = this.launch(restarted).finally(() => {
      this.launching = null;
    });
    return this.launching;
  }

  private async launch(restarted: boolean): Promise<HelloResult> {
    const revision = ++this.revision;
    this.setState({ phase: restarted ? "restarting" : "starting", generation: "" });
    let session: Session | null = null;
    try {
      session = this.spawnSession();
      const hello = await this.handlers.hello(session.client);
      if (this.stopping || revision !== this.revision) throw new Error("desktop startup cancelled");
      session.generation = hello.runtimeGeneration;
      await session.client.request("desktop/start", {}, LIFECYCLE_TIMEOUT_MS);
      if (!session.alive || this.stopping || revision !== this.revision) throw new Error("desktop service exited during startup");
      session.ready = true;
      this.hello = hello;
      this.setState({ phase: "ready", generation: hello.runtimeGeneration });
      await this.handlers.onReady(hello, restarted);
      return hello;
    } catch (error) {
      if ((!session || this.session === session) && !this.stopping && revision === this.revision) {
        this.setState({ phase: "failed", generation: "", error: errorText(error) });
        this.handlers.onFailed(error);
        if (session) void this.terminate(session).catch((error) => this.options.log.error(`service termination failed: ${errorText(error)}`));
      }
      throw error;
    }
  }

  private spawnSession(): Session {
    const { binary, args, env, log } = this.options;
    const child = this.spawnFn(binary, args, {
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
      env,
    });
    const session: Session = {
      child,
      client: null as unknown as RpcClient,
      generation: "",
      alive: true,
      ready: false,
      eventSeq: 0,
      expectExit: false,
      exited: Promise.resolve(),
    };
    session.client = new RpcClient(
      {
        write: (line) => {
          if (!child.stdin || child.stdin.destroyed) throw new Error("desktop service stdin is closed");
          child.stdin.write(line);
        },
      },
      {
        onRequest: (method, params) => this.handlers.onRequest(method, params),
        onNotification: (method, params) => this.onNotification(session, method, params),
        onProtocolError: (kind, line) => log.warn(`service stdout ${kind}: ${line.slice(0, 200)}`),
      },
    );
    session.exited = new Promise<void>((resolve) => {
      const finish = (error: Error, code: number | null, signal: NodeJS.Signals | null) => {
        if (!session.alive) return;
        session.alive = false;
        session.client.close(error);
        resolve();
        this.onExit(session, code, signal);
      };
      child.once("exit", (code, signal) => finish(new Error(`desktop service exited (${describeExit(code, signal)})`), code, signal));
      child.once("error", (error) => finish(new Error(`desktop service failed to start: ${errorText(error)}`), null, null));
    });
    child.stdout?.on("data", (chunk: Buffer) => {
      try {
        session.client.feed(chunk);
      } catch (error) {
        log.error(`service stream unusable: ${errorText(error)}`);
        child.kill();
      }
    });
    child.stderr?.on("data", (chunk: Buffer) => this.options.onStderr(chunk));
    child.stdin?.on("error", (error) => log.warn(`service stdin: ${errorText(error)}`));
    this.session = session;
    return session;
  }

  private onNotification(session: Session, method: string, params: unknown): void {
    if (method !== "desktop/event") {
      this.options.log.warn(`unknown service notification ${method}`);
      return;
    }
    const frame = eventFrame(params);
    if (!frame) {
      this.options.log.warn("malformed desktop/event frame dropped");
      return;
    }
    if (this.session !== session || !session.alive || frame.generation !== session.generation) {
      this.options.log.warn(`event ${frame.name} from dead generation ${frame.generation} dropped`);
      return;
    }
    if (frame.seq <= session.eventSeq) return;
    if (frame.seq > session.eventSeq + 1) this.options.log.warn(`desktop event gap: ${session.eventSeq} -> ${frame.seq}`);
    session.eventSeq = frame.seq;
    this.handlers.onEvent(frame);
  }

  private onExit(session: Session, code: number | null, signal: NodeJS.Signals | null): void {
    if (this.session !== session || !session.ready) return;
    const reason = describeExit(code, signal);
    if (session.expectExit || this.stopping) {
      this.setState({ phase: "exited", generation: "" });
      return;
    }
    this.options.log.error(`desktop service exited unexpectedly (${reason})`);
    if (this.budget.allow(this.now())) {
      void this.begin(true).catch(() => undefined);
      return;
    }
    const error = new Error(`desktop service exited (${reason}); automatic restarts exhausted`);
    this.setState({ phase: "failed", generation: "", error: error.message });
    this.handlers.onFailed(error);
  }

  private async terminate(session: Session): Promise<void> {
    session.expectExit = true;
    try {
      session.child.stdin?.end();
    } catch {
      // Already closed.
    }
    if (!session.alive) return;
    await Promise.race([session.exited, delay(this.exitGraceMs)]);
    if (!session.alive) return;
    this.options.log.warn("desktop service did not exit after stdin close; killing it");
    session.child.kill("SIGKILL");
    await Promise.race([session.exited, delay(1000)]);
    if (session.alive) throw new Error("desktop service did not terminate; shell exit withheld");
  }

  private setState(state: ServiceState): void {
    this.state = state;
    // A destroyed renderer or failing observer must not turn confirmed service
    // exit into a failed shutdown, nor skip the shell's remaining cleanup.
    try {
      this.handlers.onState(state);
    } catch (error) {
      this.options.log.warn(`service state observer failed: ${errorText(error)}`);
    }
  }
}
