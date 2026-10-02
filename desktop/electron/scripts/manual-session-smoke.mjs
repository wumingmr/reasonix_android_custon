// Packaged application only: no service override or development handshake bypass.
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { _electron as electron } from "playwright";
import { once } from "node:events";
import { execFileSync } from "node:child_process";

const executablePath = process.argv[2];
if (!executablePath) throw new Error("usage: node manual-session-smoke.mjs <packaged executable>");
const home = mkdtempSync(path.join(tmpdir(), "reasonix-manual-package-"));
const evidence = process.env.REASONIX_MANUAL_EVIDENCE || "/tmp/reasonix-manual-package-evidence";
mkdirSync(evidence, { recursive: true });
const env = { ...process.env, REASONIX_HOME: home, REASONIX_STATE_HOME: home, REASONIX_CACHE_HOME: path.join(home,"cache") };
delete env.REASONIX_DEV;
delete env.REASONIX_DESKTOP_SERVICE;
let shell;
const results = [];
async function launch() {
  shell = await electron.launch({ executablePath, env, timeout: 60000 });
  const page = await shell.firstWindow();
  await page.waitForURL("reasonix://app/index.html", { timeout: 60000 });
  await page.waitForFunction(() => !!window.reasonixDesktop, null, { timeout: 30000 });
  await page.waitForFunction(() => !document.querySelector(".boot-shell"), null, { timeout: 60000 });
  await page.locator(".app").waitFor({state:"visible",timeout:60000});
  // An isolated first launch opens provider setup. Leave it through its normal
  // Back control; no credentials or production configuration are installed.
  await page.addLocatorHandler(page.locator(".management-screen__back:visible"),async button=>{await button.click();});
  // The React splash is distinct from index.html's boot shell. Actionability
  // proves that an actual user can reach the welcome surface underneath it.
  await page.locator(".sidebar__quick-action:visible").first().click({trial:true,timeout:60000});
  return page;
}
const rpc = (page, command, ...args) => page.evaluate(({command,args}) => window.reasonixDesktop.invoke(command,args),{command,args});
try {
  let page = await launch();
  results.push({version:await rpc(page,"Version"),platform:await rpc(page,"Platform")});
  const operations = [];
  for (let index=0;index<3;index++) operations.push(await rpc(page,"BeginManualSessionCreation",{operationId:`packaged-manual-${index}`,workspaceId:"",scope:"global"}));
  for (const operation of operations) {
    const deadline=Date.now()+60000;
    let current=await rpc(page,"GetManualSessionCreation",operation.operationId);
    while((current.phase==="reserved" || current.phase==="starting") && Date.now()<deadline) {
      await new Promise(resolve=>setTimeout(resolve,100));
      current=await rpc(page,"GetManualSessionCreation",operation.operationId);
    }
    assert.equal(current.phase,"ready",JSON.stringify(current));
    assert.deepEqual((await rpc(page,"BeginManualSessionCreation",{operationId:operation.operationId,workspaceId:"",scope:"global"})).ref,operation.ref);
  }
  assert.equal(new Set(operations.map(op=>op.ref.sessionId)).size,3);
  assert.deepEqual(await rpc(page,"ListSessionDraftSummaries"),[]);
  const ref=operations[0].ref;
  const state=await rpc(page,"GetSessionComposerState",ref);
  const image="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWQ0AAAAASUVORK5CYII=";
  const attachment=await rpc(page,"SavePastedImageForComposerTarget",{kind:"session",session:ref},image);
  const contentJson=JSON.stringify({text:"packaged unsent input 中文",attachments:[{path:attachment,displayName:"recovered.png"}],pastedBlocks:[],workspaceRefs:[],sessionRefs:[],selectedTextRefs:[],invocations:[],goalDraft:true});
  await rpc(page,"SaveSessionComposerState",{ref,expectedRevision:state.revision,contentVersion:1,contentJson});
  await page.screenshot({path:path.join(evidence,"created.png")});
  await shell.close(); shell=undefined;
  page=await launch();
  assert.equal((await rpc(page,"GetSessionComposerState",ref)).contentJson,contentJson);
  assert.match(await rpc(page,"AttachmentDataURLForComposerTarget",{kind:"session",session:ref},attachment),/^data:image\/png;base64,/);
  const shellProcess=shell.process();
  const services=execFileSync("ps",["-axo","pid=,ppid=,comm="],{encoding:"utf8"}).split("\n").map(line=>line.trim().split(/\s+/)).filter(parts=>Number(parts[1])===shellProcess.pid && parts.slice(2).join(" ").includes("/Resources/service/reasonix-desktop")).map(parts=>Number(parts[0]));
  assert.equal(services.length,1,"identify this isolated application's service before the crash");
  const exited=once(shellProcess,"exit");
  shellProcess.kill("SIGKILL"); await exited; shell=undefined;
  const alive=pid=>{try {process.kill(pid,0);return true;} catch {return false;}};
  const deadline=Date.now()+30000;
  while(services.some(alive) && Date.now()<deadline) await new Promise(resolve=>setTimeout(resolve,100));
  assert.equal(services.some(alive),false,"orphan service must release its leases after shell crash");
  page=await launch();
  assert.equal((await rpc(page,"GetSessionComposerState",ref)).contentJson,contentJson,"crash recovers acknowledged input");
  for (const op of operations) await rpc(page,"ArchiveSessionTarget",{ref:op.ref});
  await shell.close(); shell=undefined;
  page=await launch();
  assert.equal((await rpc(page,"GetSessionComposerState",ref)).contentJson,contentJson);
  assert.deepEqual(await rpc(page,"ListSessionDraftSummaries"),[]);
  const snapshot=await rpc(page,"GetWorkspaceSnapshot");
  const identities=snapshot.workspaces.flatMap(workspace=>workspace.sessionIds);
  assert.deepEqual([...new Set(identities)].sort(),operations.map(op=>op.ref.sessionId).sort(),"restart must not create any replacement identity");
  assert.equal(identities.filter(id=>!snapshot.archivedSessionIds.includes(id)).length,0);
  assert.equal(await page.locator("textarea.composer__input:not([aria-hidden=true])").count(),0,"empty welcome must not expose an implicit input");
  await page.locator(".session-loading-indicator").waitFor({state:"hidden",timeout:10000});
  await page.screenshot({path:path.join(evidence,"archived-empty.png")});
  await shell.close(); shell=undefined;
  results.push({passed:["matched shell/service handshake","three independent sessions and retry identities","normal exit and SQLite reopen","acknowledged input survives shell crash","image reference and preview survive restart","input survives archive and restart","last archive creates no replacement"]});
  writeFileSync(path.join(evidence,"results.json"),JSON.stringify({home,executablePath,results},null,2));
  console.log(`PASS packaged creation, persistence, archive and restart: ${evidence}`);
} catch (error) {
  if (shell) {
    const page=await shell.firstWindow();
    await page.screenshot({path:path.join(evidence,"failure.png")});
    writeFileSync(path.join(evidence,"failure.txt"),await page.locator("body").innerText());
  }
  throw error;
} finally { if(shell) await shell.close(); }
