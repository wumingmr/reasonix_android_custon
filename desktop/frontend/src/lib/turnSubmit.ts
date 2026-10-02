import type { AppBindings } from "./bridge";
import type { StructuredInvocationSubmit } from "./invocationDisplay";
import type { CollaborationMode, ToolApprovalMode } from "./types";
import { submitAttachmentTurn } from "./attachmentSubmit";
import { draftSubmit } from "./draftSubmit";
import { isCompactSubmission } from "./sessionMaintenanceOperation";

export type ManagementReceipt = { operationId?: string; errorCode?: string };
type SubmitOutcome = [0] | [1, string[]] | [2, ManagementReceipt] | [3, string];

export async function submitTurn(
  app: AppBindings,
  tabId: string,
  submissionId: string,
  display: string,
  submit: string,
  original: string,
  structured?: StructuredInvocationSubmit,
  initialGoal?: { goal: string; collaborationMode: CollaborationMode; toolApprovalMode: ToolApprovalMode },
): Promise<SubmitOutcome> {
  let receipt: unknown;
  if (structured?.modelApplicationChoice) {
    if (!app.StartTurnWithModelApplication) throw new Error("Model application choice is unsupported by this host");
    const result=await app.StartTurnWithModelApplication(tabId,submissionId,{
      input:structured.input,display:structured.display,original,
      goal:initialGoal?.goal,toolApprovalMode:initialGoal?.toolApprovalMode,
      invocations:structured.invocations,attachments:structured.attachments ?? [],
    },structured.modelApplicationChoice);
    return initialGoal ? [1, []] : [3,result.turnId];
  }
  // Management commands have no durable user row to edit. Preserve the actual
  // instructions (including expanded paste blocks) through the typed admission.
  if (isCompactSubmission(submit, structured, initialGoal)) receipt = typeof app.StartTurnForTab === "function"
    ? await app.StartTurnForTab(tabId, submit, submissionId)
    : await app.SubmitToTabWithID(tabId, submit, submissionId);
  else if (structured?.attachments?.length) receipt = await submitAttachmentTurn(app, submissionId, structured, original, initialGoal);
  else if (initialGoal) {
    receipt = await app.SubmitInitialGoalToTabWithID(
      tabId,
      initialGoal.goal,
      structured?.display.trim() || display,
      structured?.input.trim() || submit,
      structured?.invocations ?? [],
      initialGoal.collaborationMode,
      initialGoal.toolApprovalMode,
      submissionId,
    );
  } else if (structured) receipt = await app.SubmitInvocationsToTabWithID(tabId, structured.display.trim(), structured.input.trim(), structured.invocations, submissionId);
  else if (original) receipt = await app.SubmitEditedDisplayToTabWithID(tabId, display, submit, original, submissionId);
  else if (display !== submit) receipt = await app.SubmitDisplayToTabWithID(tabId, display, submit, submissionId);
  else receipt = await draftSubmit(app, tabId, submit, submissionId);
  if (initialGoal) return [1, Array.isArray(receipt) ? receipt : []];
  if (receipt && typeof receipt === "object" && "disposition" in receipt && receipt.disposition === "management_handled") return [2, {
    operationId: "operationId" in receipt && typeof receipt.operationId === "string" ? receipt.operationId : undefined,
    errorCode: "managementErrorCode" in receipt && typeof receipt.managementErrorCode === "string" ? receipt.managementErrorCode : undefined,
  }];
  if (receipt && typeof receipt === "object" && "turnId" in receipt && typeof receipt.turnId === "string") return [3, receipt.turnId];
  return [0];
}
