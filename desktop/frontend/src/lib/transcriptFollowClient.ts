import type { TranscriptFollowResponse, FollowRequest } from "../generated/desktopContract.generated";
import { addBreadcrumb } from "./breadcrumbs";
import {
  clearTranscriptDiagnostic,
  newTranscriptDiagnosticOwner,
  publishTranscriptDiagnostic,
  type TranscriptDiagnostic,
  type TranscriptDiagnosticReason,
  type TranscriptDiagnosticStage,
} from "./transcriptDiagnostics";

export type TranscriptConnection = "syncing" | "connected" | "disconnected";
type Change = NonNullable<TranscriptFollowResponse["changes"]>[number];
type FollowGeneration = { closeSubscription: boolean };

export interface FollowConsumer {
  install(response: TranscriptFollowResponse): Promise<void> | void;
  changes(changes: Change[]): void;
  connection(state: TranscriptConnection, error?: string): void;
}

type TranscriptFollowFailureState = {
  stage: TranscriptDiagnosticStage;
  reason: TranscriptDiagnosticReason;
  firstAt: number;
  lastReportedAt: number;
  failures: number;
  errorType: TranscriptDiagnostic["errorType"];
};

export interface TranscriptFollowClientOptions {
  transport?: "local" | "remote";
  now?: () => number;
  onDiagnostic?: (event: TranscriptDiagnostic, visible: boolean) => void;
}

class TranscriptFollowFailure extends Error {
  constructor(
    readonly stage: TranscriptDiagnosticStage,
    readonly reason: TranscriptDiagnosticReason,
    readonly errorType: TranscriptDiagnostic["errorType"],
    message: string,
  ) {
    super(message);
  }
}

function failure(stage: TranscriptDiagnosticStage, reason: TranscriptDiagnosticReason, message: string, cause?: unknown): TranscriptFollowFailure {
  const errorType = cause instanceof Error ? "error"
    : typeof cause === "string" ? "string"
    : cause && typeof cause === "object" ? "object"
    : cause === undefined ? "classified" : "unknown";
  return new TranscriptFollowFailure(stage, reason, errorType, message);
}

function classifiedFailure(error: unknown, stage: TranscriptDiagnosticStage, reason: TranscriptDiagnosticReason): TranscriptFollowFailure {
  if (error instanceof TranscriptFollowFailure) return error;
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "unknown transcript failure";
  return failure(stage, reason, message, error);
}

/** One ordered consumer for both transports. Connection failures only request
 * another snapshot; this class has no model, submit, stop or retry-turn API. */
export class TranscriptFollowClient {
  private generation: FollowGeneration = { closeSubscription: true };
  private subscription = "";
  private revision = 0;
  private coverage = 0;
  private identity = "";
  private readonly indexes = new Map<string, number>();
  private readonly attemptMessages = new Map<string, string>();
  private readonly results = new Map<string, number>();
  private readonly diagnosticOwner = newTranscriptDiagnosticOwner();
  private readonly transport: "local" | "remote";
  private readonly now: () => number;
  private readonly onDiagnostic?: (event: TranscriptDiagnostic, visible: boolean) => void;
  private failureState: TranscriptFollowFailureState | null = null;

  constructor(private readonly read: (request: FollowRequest) => Promise<TranscriptFollowResponse>, options: TranscriptFollowClientOptions = {}) {
    this.transport = options.transport ?? "local";
    this.now = options.now ?? (() => Date.now());
    this.onDiagnostic = options.onDiagnostic;
  }

  async start(consumer: FollowConsumer): Promise<void> {
    this.stop();
    const generation = this.generation;
    consumer.connection("syncing");
    try { await this.baseline(generation, consumer); }
    catch (error) {
      if (generation === this.generation) {
        consumer.connection("disconnected", String(error));
        this.noteFailure(classifiedFailure(error, "baseline_read", "unknown"));
      }
      throw error;
    }
    if (generation === this.generation) void this.follow(generation, consumer);
  }

  stop(closeSubscription = true, reason?: "service_stopping"): void {
    const generation = this.generation;
    generation.closeSubscription = closeSubscription;
    this.generation = { closeSubscription: true };
    const subscription = this.subscription;
    this.subscription = "";
    this.closeSubscription(subscription, generation);
    if (reason === "service_stopping") {
      this.publish("stopped", this.failureState?.stage ?? "none", "service_stopping", true);
      this.failureState = null;
    } else if (!reason) {
      clearTranscriptDiagnostic(this.diagnosticOwner);
      this.failureState = null;
    }
  }

  private closeSubscription(subscription: string | undefined, generation: FollowGeneration): void {
    if (subscription && generation.closeSubscription) void this.read({ subscription, close: true }).catch(() => undefined);
  }

  private async baseline(generation: FollowGeneration, consumer: FollowConsumer): Promise<void> {
    let response: TranscriptFollowResponse;
    try {
      response = await this.read({});
    } catch (error) {
      throw classifiedFailure(error, "baseline_read", "transport_rejected");
    }
    if (generation !== this.generation) {
      this.closeSubscription(response.subscription, generation);
      return;
    }
    if (response.protocolVersion !== 2) {
      this.closeSubscription(response.subscription, generation);
      throw failure("baseline_validate", "protocol_version", "Transcript v2 is required. Upgrade Desktop and Serve together.");
    }
    if (!response.snapshot || !response.subscription) {
      this.closeSubscription(response.subscription, generation);
      throw failure("baseline_validate", "snapshot_missing", "Transcript v2 snapshot or subscription is missing.");
    }
    const snapshot = response.snapshot;
    const identity = JSON.stringify(snapshot.identity);
    if (identity === this.identity && snapshot.projectionRevision < this.revision) {
      this.closeSubscription(response.subscription, generation);
      throw failure("baseline_validate", "revision_regressed", "transcript snapshot revision regressed");
    }
    if (response.history && response.history.status !== "ready") {
      this.closeSubscription(response.subscription, generation);
      throw failure("baseline_validate", "history_not_ready", `transcript history ${response.history.status}`);
    }
    try { await consumer.install(response); } catch (error) {
      this.closeSubscription(response.subscription, generation);
      throw classifiedFailure(error, "snapshot_install", "consumer_error");
    }
    if (generation !== this.generation) {
      this.closeSubscription(response.subscription, generation);
      return;
    }
    this.identity = identity;
    this.subscription = response.subscription;
    this.revision = snapshot.projectionRevision;
    this.coverage = snapshot.coveredThroughSeq;
    this.indexes.clear();
    this.attemptMessages.clear();
    this.results.clear();
    for (const attempt of snapshot.activeAttempts ?? []) {
      this.indexes.set(attempt.id, attempt.nextIndex ?? 0);
      this.attemptMessages.set(attempt.id, attempt.messageId);
    }
    if (!this.failureState) addBreadcrumb("transcript.v2", `snapshot epoch=${snapshot.identity.runtimeEpoch} revision=${this.revision} commit=${this.coverage} durable=${snapshot.durableSeq} records=${snapshot.totalRecords} attempts=${this.indexes.size}`);
    consumer.connection("connected");
    // Installing a replacement snapshot does not prove that delta following
    // recovered. Keep this fault segment until a full follow succeeds.
  }

  private validate(changes: Change[]): Change[] {
    let revision = this.revision;
    let coverage = this.coverage;
    const indexes = new Map(this.indexes);
    const attempts = new Map(this.attemptMessages);
    const results = new Map(this.results);
    const accepted: Change[] = [];
    // Transport coalescing can reorder frames within a delivered batch. Their
    // publisher revisions establish order; an actual missing revision resets.
    for (const change of [...changes].sort((a, b) => a.revision - b.revision)) {
      if (change.revision <= revision) continue;
      if (change.resetRequired || change.revision !== revision + 1) throw failure("delta_validate", "revision_gap", "transcript revision gap");
      if (change.firstSeq) {
        if (change.firstSeq !== coverage + 1 || change.commitSeq < change.firstSeq) throw failure("delta_validate", "business_gap", "transcript business gap");
        coverage = change.commitSeq;
      } else if (change.commitSeq !== coverage) throw failure("delta_validate", "frame_cut_mismatch", "transcript frame cut mismatch");
      const event = change.event;
      for (const record of change.records ?? []) if (record.messageId) results.set(record.messageId, change.commitSeq);
      while (results.size > 192) results.delete(results.keys().next().value!);
      if (event?.kind === "stream_attempt" && event.streamAttempt?.action === "begin") {
        if (!event.messageId) throw failure("delta_validate", "sampling_identity_missing", "transcript sampling identity missing");
        indexes.set(event.streamAttempt.id, 0);
        attempts.set(event.streamAttempt.id, event.messageId);
      }
      if (change.attemptId && !change.resultSeq && event?.kind !== "stream_attempt") {
        if (indexes.get(change.attemptId) !== change.index) throw failure("delta_validate", "sampling_gap", "transcript sampling gap");
        indexes.set(change.attemptId, change.index + 1);
      }
      if (change.resultSeq && (change.resultSeq > coverage || !["message/complete", "message/interrupted"].includes(change.resultKind ?? ""))) throw failure("delta_validate", "settlement_not_committed", "transcript settlement is not committed");
      if (change.resultSeq) {
        const message = attempts.get(change.attemptId ?? "");
        if (!message || message !== event?.messageId || (results.has(message) && results.get(message) !== change.resultSeq)) throw failure("delta_validate", "settlement_identity_mismatch", "transcript settlement identity mismatch");
      }
      if (event?.kind === "stream_attempt" && event.streamAttempt?.action !== "begin") {
        indexes.delete(event.streamAttempt?.id ?? ""); attempts.delete(event.streamAttempt?.id ?? "");
      }
      revision = change.revision;
      accepted.push(change);
    }
    this.revision = revision;
    this.coverage = coverage;
    this.indexes.clear();
    for (const [id, index] of indexes) this.indexes.set(id, index);
    this.attemptMessages.clear(); for (const [id, message] of attempts) this.attemptMessages.set(id, message);
    this.results.clear(); for (const [id, sequence] of results) this.results.set(id, sequence);
    return accepted;
  }

  private async follow(generation: FollowGeneration, consumer: FollowConsumer): Promise<void> {
    while (generation === this.generation) {
      try {
        if (!this.subscription) await this.baseline(generation, consumer);
        if (generation !== this.generation) return;
        let response: TranscriptFollowResponse;
        try {
          response = await this.read({ subscription: this.subscription, afterRevision: this.revision });
        } catch (error) {
          throw classifiedFailure(error, "delta_read", "transport_rejected");
        }
        if (generation !== this.generation) return;
        if (response.protocolVersion !== 2) throw failure("delta_validate", "protocol_version", "transcript protocol version changed");
        if (response.resetRequired) throw failure("delta_validate", "resync_required", "transcript requires resynchronization");
        const changes = this.validate(response.changes ?? []);
        try {
          consumer.changes(changes);
        } catch (error) {
          throw classifiedFailure(error, "delta_apply", "consumer_error");
        }
        consumer.connection("connected");
        this.noteRecovery();
      } catch (error) {
        if (generation !== this.generation) return;
        const transcriptFailure = classifiedFailure(error, "delta_read", "unknown");
        const old = this.subscription;
        this.subscription = "";
        this.closeSubscription(old, generation);
        consumer.connection("disconnected", String(error));
        this.noteFailure(transcriptFailure);
        await new Promise(resolve => setTimeout(resolve, 1000));
        if (generation === this.generation) consumer.connection("syncing");
      }
    }
  }

  private noteFailure(problem: TranscriptFollowFailure): void {
    const now = this.now();
    const previous = this.failureState;
    if (!previous) {
      this.failureState = { stage: problem.stage, reason: problem.reason, errorType: problem.errorType, firstAt: now, lastReportedAt: now, failures: 1 };
      this.publish("failure", problem.stage, problem.reason, true);
      return;
    }
    if (previous.stage !== problem.stage || previous.reason !== problem.reason) {
      this.failureState = {
        stage: problem.stage,
        reason: problem.reason,
        errorType: problem.errorType,
        firstAt: previous.firstAt,
        lastReportedAt: now,
        failures: previous.failures + 1,
      };
      this.publish("failure", problem.stage, problem.reason, true);
      return;
    }
    previous.failures++;
    previous.errorType = problem.errorType;
    const visible = now - previous.lastReportedAt >= 30_000;
    if (visible) previous.lastReportedAt = now;
    this.publish(visible ? "summary" : "failure", previous.stage, previous.reason, visible);
  }

  private noteRecovery(): void {
    if (!this.failureState) return;
    this.publish("recovered", this.failureState.stage, this.failureState.reason, true);
    this.failureState = null;
  }

  private publish(
    event: TranscriptDiagnostic["event"],
    stage: TranscriptDiagnosticStage,
    reason: TranscriptDiagnosticReason,
    visible: boolean,
  ): void {
    const state = this.failureState;
    const now = this.now();
    const diagnostic: TranscriptDiagnostic = {
      kind: "transcript",
      event,
      stage,
      reason,
      transport: this.transport,
      errorType: state?.errorType ?? "classified",
      revision: Math.max(0, this.revision),
      commit: Math.max(0, this.coverage),
      attempts: this.indexes.size,
      failures: state?.failures ?? 0,
      durationMs: state ? Math.max(0, now - state.firstAt) : 0,
    };
    publishTranscriptDiagnostic(this.diagnosticOwner, diagnostic, visible);
    this.onDiagnostic?.(diagnostic, visible);
  }
}
