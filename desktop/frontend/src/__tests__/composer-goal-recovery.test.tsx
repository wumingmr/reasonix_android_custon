import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { emptySessionInput, flushAllSessionComposers, resumeSessionComposerEditing } from "../lib/sessionComposerPersistence";

const dom=installDom();
const backend=makeSessionUIMock(async()=>{});
installBridgeApp({...backend,GetSessionComposerState:async (ref:Parameters<typeof backend.GetSessionComposerState>[0])=>{
 const result=await backend.GetSessionComposerState(ref);
 return {...result, historyChanged:ref.sessionId==="advanced",submissionId:ref.sessionId==="unknown"?"pending":""};
}});
for (const sessionId of ["advanced","unknown","live","editable"]) {
 await backend.SaveSessionComposerState({ref:{hostId:"local",sessionId},expectedRevision:"0",contentVersion:1,contentJson:JSON.stringify({...emptySessionInput(),goalDraft:true})});
}
const modes:string[]=[];
const {root,rerender}=await renderComposer({tabId:"advanced",sessionKey:"advanced",formalSessionRef:{hostId:"local",sessionId:"advanced"},onSetCollaborationMode:mode=>modes.push(mode)});
try {
 for (const id of ["advanced","unknown","live"]) {
  await rerender({tabId:id,sessionKey:id,formalSessionRef:{hostId:"local",sessionId:id},goal:id==="live"?"existing active goal":""});
  await act(async()=>new Promise(resolve=>setTimeout(resolve,10)));
  assert.equal(modes.length,0,`${id} must not reactivate or clear the live goal`);
 }
 await rerender({tabId:"editable",sessionKey:"editable",formalSessionRef:{hostId:"local",sessionId:"editable"},goal:""});
 await act(async()=>new Promise(resolve=>setTimeout(resolve,10)));
 assert.deepEqual(modes,["goal"]);
 await rerender({collaborationMode:"goal",goal:"now active"});
 const stop=document.querySelector<HTMLButtonElement>(".composer-task-mode-trigger");
 assert.ok(stop,"recovered goal draft has its cancel control");
 await act(async()=>stop.click());
 await act(async()=>{await flushAllSessionComposers();resumeSessionComposerEditing();});
 assert.equal(JSON.parse((await backend.GetSessionComposerState({hostId:"local",sessionId:"editable"})).contentJson).goalDraft,false);
 console.log("PASS Goal restore guards: unknown send, changed history, active goal and persisted cancellation");
} finally {await act(async()=>root.unmount());resumeSessionComposerEditing();dom.window.close();}
