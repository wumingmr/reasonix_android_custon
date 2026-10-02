import assert from "node:assert/strict";
import { appliedOnce, definitelyNotAccepted, modelApplicationError, type ModelApplicationDetails } from "../lib/modelApplication";
import { isUnknownSubmissionError } from "../lib/localSubmissionState";
import { submitTurn } from "../lib/turnSubmit";
import { followupNotSubmitted } from "../lib/pendingFollowup";
import type { AppBindings } from "../lib/bridge";

const details:ModelApplicationDetails={code:"model_settings_pending",runtimeIdentity:"runtime-a",appliedRevision:"a",desiredRevision:"b",model:"p/m",blockingJobs:[],canUseApplied:true};
const error=Object.assign(new Error("network settings could not apply"),{data:{submissionOutcome:"not_accepted",modelApplication:details}});
assert.equal(modelApplicationError(error),details);
assert.equal(definitelyNotAccepted(error),true);
assert.equal(isUnknownSubmissionError(error),false,"structured outcome wins over English error text");
assert.equal(isUnknownSubmissionError(Object.assign(new Error("settlement failed"),{data:{submissionOutcome:"unknown"}})),true);
assert.equal(followupNotSubmitted(Object.assign(new Error("reasonix_error:channel_read_only"), {data:{submissionOutcome:"unknown"}})), false, "structured uncertainty overrides legacy rejection text");
const calls:unknown[]=[];
const bindings={
 StartTurnWithModelApplication:async(...args:unknown[])=>{calls.push(args);return {turnId:"turn"};},
 SubmitInvocationsToTabWithID:async(...args:unknown[])=>{calls.push(args);return {turnId:"next"};},
} as unknown as AppBindings;
const structured={input:"exact model input",display:"shown input",invocations:[],modelApplicationChoice:appliedOnce(details)};
assert.deepEqual(await submitTurn(bindings,"tab","first","shown input","exact model input","",structured),[3,"turn"]);
const first=calls[0] as unknown[];
assert.equal((first[2] as {input:string}).input,"exact model input");
assert.equal((first[2] as Record<string,unknown>).modelApplicationChoice,undefined,"choice stays outside model input");
assert.deepEqual(first[3],appliedOnce(details));
await submitTurn(bindings,"tab","second","next","next","",{input:"next",display:"next",invocations:[]});
assert.deepEqual(calls[1],["tab","next","next",[],"second"],"the next submit uses default latest admission");
console.log("model application: structured rejection and submission-scoped choice passed");
