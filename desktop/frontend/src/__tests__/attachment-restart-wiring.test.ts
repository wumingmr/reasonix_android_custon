import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { stageImageFile, prepareImageSubmission, restoreExternalFolderReferences } from "../lib/attachmentSubmit";
import type { AppBindings } from "../lib/bridge";

const dom = new JSDOM();
globalThis.FileReader = dom.window.FileReader;
const target = {kind:"session", tabId:"source-tab", session:{hostId:"local",sessionId:"source-session"}};
const captures: unknown[] = [], stages: unknown[][] = [], released: string[] = [];
let savedData = "", restoredPath = "__reasonix_external_folder/hash/folder";
const app = {
  CaptureAttachmentTarget: async (value: unknown) => {captures.push(value);return {token:"source-token",capabilities:["attachments-v2"]};},
  StageImageForTarget: async (...args: unknown[]) => { stages.push(args);return {draftId:`temporary-${stages.length}`};},
  ReadDraftImageForTarget: async () => "data:image/png;base64,cHJldmlldw==",
  SavePastedImageForComposerTarget: async (value:unknown, data:string) => {assert.deepEqual(value,target);savedData=data;return ".reasonix/attachments/durable.png";},
  AttachmentDataURLForTarget: async (token:string,path:string) => {assert.equal(token,"source-token");assert.equal(path,".reasonix/attachments/durable.png");return savedData;},
  ReleaseAttachmentTarget: async (token:string) => {released.push(token);},
  AttachDroppedForTarget: async (token:string,path:string) => {assert.equal(token,"source-token");assert.equal(path,"/external/folder");return {kind:"workspace",isDir:true,path:restoredPath};},
} as unknown as AppBindings;
try {
  const file = new dom.window.File(["original image"], "image.png", {type:"image/png",lastModified:1});
  const staged = await stageImageFile(app,"source-token","source-session",file,target);
  assert.equal(staged.recoveryPath,".reasonix/attachments/durable.png");
  assert.equal(savedData,"data:image/png;base64,b3JpZ2luYWwgaW1hZ2U=","persist original bytes, not a resized preview");
  // Simulate another process: no RAM-only draft credential survives.
  const prepared = await prepareImageSubmission(app,target,"source-session","input-version",[{recoveryPath:staged.recoveryPath,displayName:"image.png"}],undefined,"describe","describe");
  assert.equal(prepared.structured.attachments[0].draftId,"temporary-2");
  assert.equal(stages[1][4],savedData);
  assert.deepEqual(captures[0],target);
  assert.equal(prepared.structured.input,"describe","recovery adds no provider prompt bytes");
  const external = [{path:restoredPath,isDir:true,displayPath:"/external/folder"}];
  await restoreExternalFolderReferences(app,target,external);
  restoredPath="__reasonix_external_folder/changed/folder";
  await assert.rejects(restoreExternalFolderReferences(app,target,external),/folder changed/);
  assert.equal(released.length,2,"both successful and failed folder captures are released");
  await assert.rejects(prepareImageSubmission({...app,AttachmentDataURLForTarget:async()=>{throw Error("file missing");}},target,"other-session","v1",[{recoveryPath:staged.recoveryPath}],undefined,"x","x"),/file missing/);
  assert.equal(released.length,3,"failed image restoration releases the captured target");
  console.log("PASS attachment restart: original bytes, new credential, captured identity, external folder restoration and failed-source cleanup");
} finally { dom.window.close(); }
