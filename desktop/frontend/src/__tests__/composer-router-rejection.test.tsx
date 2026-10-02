import assert from "node:assert/strict";
import React,{act} from "react";
import {createRoot} from "react-dom/client";
import {installDom,installBridgeApp} from "./composerInboxHarness";
import {makeSessionUIMock} from "../lib/sessionUIMock";
import {useComposerRouter, type ComposerRouterInput} from "../app-runtime/useComposerRouter";
import {sendPersistedComposer,useSessionComposerPersistence} from "../lib/sessionComposerPersistence";
const dom=installDom();
installBridgeApp({...makeSessionUIMock(async()=>{})});
let applied=false;
let editor!:ReturnType<typeof useSessionComposerPersistence>,router!:ReturnType<typeof useComposerRouter>;
const noop=()=>{};
const ports:ComposerRouterInput["ports"]={runShellForTab:async()=>{},switchModel:async()=>applied,newSession:async()=>{},setSettingsTarget:noop,setClearContextPending:noop,clearWorkspaceConflict:noop,setWorkspaceConflict:noop,setPendingClose:noop,submitComposerTurn:async()=>{throw Error("must not dispatch a model command to provider");},steerForTab:async()=>{},isRemoteTab:()=>false};
function Probe(){editor=useSessionComposerPersistence({hostId:"local",sessionId:"command"},"command-tab");router=useComposerRouter({activeTabId:"command-tab",goalDraftActive:false,t:key=>key,notice:noop,showToast:noop,ports});return null;}
const root=createRoot(document.getElementById("root")!);
try {
 await act(async()=>{root.render(React.createElement(Probe));await new Promise(resolve=>setTimeout(resolve,0));});
 const owner=editor.target!;
 await act(async()=>owner.onPatch!(owner.draftId,owner.generation,{text:"/model configured/model"}));
 await act(async()=>assert.rejects(sendPersistedComposer("command-tab","/model configured/model","/model configured/model","failed-command",()=>router.handleSend("/model configured/model")),/inbox_not_submitted/));
 assert.equal(editor.blocked,false);assert.equal(editor.target!.initial.text,"/model configured/model");
 applied=true;
 await act(async()=>{await sendPersistedComposer("command-tab","/model configured/model","/model configured/model","success-command",()=>router.handleSend("/model configured/model"));});
 assert.equal(editor.target!.initial.text,"");
 console.log("PASS /model settles saved input only after an applied switch");
} finally {await act(async()=>root.unmount());dom.window.close();}
