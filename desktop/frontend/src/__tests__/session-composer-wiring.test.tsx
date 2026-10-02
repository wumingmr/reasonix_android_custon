import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { flushAllSessionComposers, resumeSessionComposerEditing, sendPersistedComposer, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";

const dom = new JSDOM("<div id='root'></div>",{url:"http://localhost"});
Object.assign(globalThis,{window:dom.window,document:dom.window.document,IS_REACT_ACT_ENVIRONMENT:true});
const backend=makeSessionUIMock(async()=>{});
const previews:string[]=[];
installDesktopHostStub({...backend,AttachmentDataURLForComposerTarget:async (_target:unknown,path:string)=>{previews.push(path);return "data:image/png;base64,aW1hZ2U=";}});
let editor!:ReturnType<typeof useSessionComposerPersistence>;
function Probe({id}:{id:string}) {editor=useSessionComposerPersistence({hostId:"local",sessionId:id},id);return null;}
const root=createRoot(document.getElementById("root")!);
async function paint(id:string){await act(async()=>{root.render(React.createElement(Probe,{id}));await new Promise(resolve=>setTimeout(resolve,0));});}
async function flush(){await act(async()=>{await flushAllSessionComposers();resumeSessionComposerEditing();});}
try {
 await paint("wiring");
 const owner=editor.target!;
 await act(async()=>owner.onPatch!(owner.draftId,owner.generation,{text:"retained",attachments:[{path:"draft:temporary",draftId:"temporary",recoveryPath:".reasonix/attachments/kept.png",displayName:"kept.png",previewUrl:"blob:ephemeral"}]}));
 await flush();
 const saved=JSON.parse((await backend.GetSessionComposerState({hostId:"local",sessionId:"wiring"})).contentJson);
 assert.equal(saved.attachments[0].path,".reasonix/attachments/kept.png");
 assert.equal(saved.attachments[0].draftId,undefined);
 assert.equal(saved.attachments[0].previewUrl,undefined);
 await backend.SaveSessionComposerState({ref:{hostId:"local",sessionId:"wiring-restored"},expectedRevision:"0",contentJson:JSON.stringify(saved),contentVersion:1});
 await paint("wiring-restored");
 assert.equal(editor.target!.initial.attachments[0].previewUrl,"data:image/png;base64,aW1hZ2U=");
 assert.ok(previews.includes(".reasonix/attachments/kept.png"));
 await paint("wiring");

 for (const code of ["inbox_capacity_items","inbox_capacity_bytes","inbox_item_too_large","channel_read_only","workspace_starting","workspace_start_failed","image_attachment_unreadable"]) {
  await act(async()=>{await assert.rejects(sendPersistedComposer("wiring","retained","retained",code,async()=>{throw Error(`reasonix_error:${code}`);}));});
  assert.equal(editor.blocked,false,`${code} is a definite rejection, not an unknown execution`);
  assert.equal(editor.target!.initial.text,"retained");
 }
 await act(async()=>{await assert.rejects(sendPersistedComposer("wiring","retained","retained","lost-followup",async()=>{throw Error("lost RPC");},undefined,undefined,"guidance"));});
 assert.equal(editor.blocked,true);
 await act(async()=>editor.settleSubmission("lost-followup"));
 assert.equal(editor.blocked,false,"confirmed inbox receipt must settle the saved input as well");
 assert.equal(editor.target!.initial.text,"");
 const nextOwner=editor.target!;
 await act(async()=>nextOwner.onPatch!(nextOwner.draftId,nextOwner.generation,{text:"new input after recovery"}));
 await act(async()=>editor.settleSubmission("lost-followup"));
 assert.equal(editor.target!.initial.text,"new input after recovery","a repeated follow-up check cannot clear newer unsaved input");
 await flush();
 console.log("PASS persistent composer wiring: durable image source, explicit rejection and follow-up receipt settlement");
} finally {await act(async()=>root.unmount());resumeSessionComposerEditing();dom.window.close();}
