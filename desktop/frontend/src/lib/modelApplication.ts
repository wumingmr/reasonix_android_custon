export type ModelApplicationChoice = {
  mode: "latest" | "applied_once";
  expectedAppliedRevision: string;
  expectedDesiredRevision: string;
  expectedRuntimeIdentity: string;
	confirmationToken?:string;
};

export type ModelApplicationDetails = {
  code: string; runtimeIdentity: string; appliedRevision: string; desiredRevision: string;
  model: string; blockingJobs: {id:string; kind:string; label:string; status:string}[];
  canUseApplied: boolean; continuationUnavailable?:string;
  connectionTarget?:string;
	confirmationToken?:string;
	applying?:boolean; availableActions?:string[];
};

export function modelApplicationError(error: unknown): ModelApplicationDetails | undefined {
  const data=(error as {data?:{modelApplication?:ModelApplicationDetails}} | undefined)?.data?.modelApplication;
  return data && typeof data.runtimeIdentity==="string" && Array.isArray(data.blockingJobs) ? data : undefined;
}

export function definitelyNotAccepted(error: unknown): boolean {
  return submissionOutcome(error) === "not_accepted";
}

export function submissionOutcome(error: unknown): "not_accepted" | "unknown" | "accepted" | undefined {
  const outcome = (error as { data?: { submissionOutcome?: string } } | undefined)?.data?.submissionOutcome;
  return outcome === "not_accepted" || outcome === "unknown" || outcome === "accepted" ? outcome : undefined;
}

export function appliedOnce(details: ModelApplicationDetails): ModelApplicationChoice {
  return {mode:"applied_once",expectedAppliedRevision:details.appliedRevision,expectedDesiredRevision:details.desiredRevision,expectedRuntimeIdentity:details.runtimeIdentity,...(details.confirmationToken ? {confirmationToken:details.confirmationToken} : {})};
}
