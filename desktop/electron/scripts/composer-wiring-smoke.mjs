// Actual packaged renderer + matching Go host, with disposable data and no provider calls.
import assert from "node:assert/strict";
import {mkdtempSync,mkdirSync,writeFileSync} from "node:fs";
import {tmpdir} from "node:os";
import path from "node:path";
import {_electron as electron} from "playwright";
const executablePath=process.argv[2];
if(!executablePath)throw Error("usage: composer-wiring-smoke.mjs <packaged executable>");
const home=mkdtempSync(path.join(tmpdir(),"reasonix-composer-wiring-"));
const evidence=process.env.REASONIX_WIRING_EVIDENCE || "/tmp/reasonix-composer-wiring-evidence";
mkdirSync(evidence,{recursive:true});
const env={...process.env,REASONIX_HOME:home,REASONIX_STATE_HOME:home,REASONIX_CACHE_HOME:path.join(home,"cache")};
delete env.REASONIX_DEV;delete env.REASONIX_DESKTOP_SERVICE;
let shell;
const rpc=(page,command,...args)=>page.evaluate(({command,args})=>window.reasonixDesktop.invoke(command,args),{command,args});
async function launch(){
 shell=await electron.launch({executablePath,env,timeout:60000});
 const page=await shell.firstWindow();
 await page.waitForURL("reasonix://app/index.html",{timeout:60000});
 await page.locator(".app").waitFor({timeout:60000});
 await page.addLocatorHandler(page.locator(".management-screen__back:visible"),async b=>b.click());
 await page.locator(".sidebar__quick-action:visible").first().click({trial:true,timeout:60000});
 return page;
}
try {
 let page=await launch();
 await page.locator(".sidebar__quick-action:visible").first().click();
 const composer=page.locator("textarea.composer__input:not([aria-hidden=true])");
 await composer.waitFor();
 await page.waitForFunction(()=>document.querySelector("textarea.composer__input:not([aria-hidden=true])")?.disabled===false);
 await composer.fill("Keep this image across restart 中文");
 // Valid source used by the host fixtures; use the renderer's normal paste path.
 const image="iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
 await composer.evaluate((element,data)=>{
  const file=new File([Uint8Array.from(atob(data),c=>c.charCodeAt(0))],"persisted.png",{type:"image/png"});
  const transfer=new DataTransfer();transfer.items.add(file);
  element.dispatchEvent(new ClipboardEvent("paste",{bubbles:true,cancelable:true,clipboardData:transfer}));
 },image);
 await page.locator(".composer-context__item").waitFor();
 const active=(await rpc(page,"ListTabs")).find(tab=>tab.active);
 assert.ok(active?.sessionId);
 const ref={hostId:"local",sessionId:active.sessionId};
 let saved;
 const deadline=Date.now()+30000;
 do {
  saved=JSON.parse((await rpc(page,"GetSessionComposerState",ref)).contentJson);
  if(saved.attachments?.[0]?.recoveryPath)break;
  await new Promise(resolve=>setTimeout(resolve,100));
 } while(Date.now()<deadline);
 assert.ok(saved.attachments?.[0]?.recoveryPath,"wait for the host's acknowledged attachment source");
 for(let i=0;i<3;i++)assert.deepEqual(JSON.parse((await rpc(page,"GetSessionComposerState",ref)).contentJson),saved,"acknowledged data remains visible to repeated RPC reads");
 assert.equal(saved.attachments[0].draftId,undefined);
 assert.equal(saved.attachments[0].previewUrl,undefined);
 await shell.close();shell=undefined;
 page=await launch();
 await page.locator(".composer-context__item img").waitFor({timeout:30000});
 await page.waitForFunction(()=>Array.from(document.querySelectorAll(".composer-context__item img")).some(img=>img.complete && img.naturalWidth>0));
 assert.equal(await page.locator("textarea.composer__input:not([aria-hidden=true])").inputValue(),saved.text);
 const current=(await rpc(page,"ListTabs")).find(tab=>tab.sessionId===ref.sessionId);
 const captured=await rpc(page,"CaptureAttachmentTarget",{kind:"session",tabId:current.id,session:ref});
 const data=await rpc(page,"AttachmentDataURLForTarget",captured.token,saved.attachments[0].recoveryPath);
 const restored=await rpc(page,"StageImageForTarget",captured.token,"restart-image-check","persisted.png","image/png",data);
 assert.ok(restored.draftId,"restored image must acquire a fresh runtime credential");
 assert.match(await rpc(page,"ReadDraftImageForTarget",captured.token,restored.draftId),/^data:image\//);
 await rpc(page,"ReleaseAttachmentTarget",captured.token);
 await page.screenshot({path:path.join(evidence,"restored-image.png")});
 await shell.close();shell=undefined;
 writeFileSync(path.join(evidence,"results.json"),JSON.stringify({home,executablePath,passed:["actual manual New click","actual renderer image paste","durable source without transient credential","normal packaged exit and restart","restored text and image preview","fresh typed image credential after restart"]},null,2));
 console.log(`PASS packaged composer wiring: ${evidence}`);
} catch(error) {
 if(shell){const page=await shell.firstWindow();await page.screenshot({path:path.join(evidence,"failure.png")});writeFileSync(path.join(evidence,"failure.txt"),await page.locator("body").innerText());}
 throw error;
} finally {if(shell)await shell.close();}
