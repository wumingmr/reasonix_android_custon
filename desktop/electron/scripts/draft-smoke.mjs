// Real Electron + Go service, disposable storage, no provider invocation.
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { _electron } from 'playwright';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const home = mkdtempSync(join(tmpdir(), 'reasonix-draft-native-'));
const workspace = join(home, 'workspace');
mkdirSync(workspace);
const report = { home, checks: [] };
const check = name => { report.checks.push(name); console.log(`PASS ${name}`); };
const launch = () => _electron.launch({ args: [root], env: { ...process.env,
  REASONIX_HOME: home, REASONIX_STATE_HOME: home, REASONIX_CACHE_HOME: join(home,'cache'),
  REASONIX_DEV: '1', REASONIX_DESKTOP_SERVICE: resolve(root,'../build/bin/reasonix-desktop-service') }, timeout:60000 });
async function ready(app) {
  const page = await app.firstWindow();
  await page.waitForFunction(() => Boolean(window.reasonixDesktop) && !document.querySelector('.boot-shell'), null, { timeout:60000 });
  await page.waitForFunction(async () => { try { await window.reasonixDesktop.invoke('ListTabs', []); return true; } catch { return false; } }, null, { timeout:60000 });
  return page;
}
const invoke = (page, name, args=[]) => page.evaluate(({name,args}) => window.reasonixDesktop.invoke(name,args),{name,args});
const installClipboardFixture = shell => shell.evaluate(async ({clipboard,ClipboardItem}) => {
  globalThis.__draftSmokeClipboard = await Promise.all((await clipboard.read()).map(async item => new ClipboardItem(Object.fromEntries(await Promise.all(item.types.map(async type => [type, await item.getType(type)]))))));
  const png = new Blob([Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aY1sAAAAASUVORK5CYII=','base64')],{type:'image/png'});
  // Explicit PNG matches the OS screenshot flavor consumed by the service.
  await clipboard.write([new ClipboardItem({'electron application/osclipboard;format="public.png"': png})]);
});
const restoreClipboard = shell => shell.evaluate(async ({clipboard}) => {
  await clipboard.write(globalThis.__draftSmokeClipboard);
  delete globalThis.__draftSmokeClipboard;
});
let shell;
try {
  shell = await launch();
  let page = await ready(shell);
  const before = await invoke(page,'ListTabs');
  const drafts = await Promise.all(Array.from({length:20}, () => invoke(page,'OpenSessionDraftForTarget',['project',workspace])));
  assert.equal(new Set(drafts.map(draft=>draft.id)).size,1);
  assert.equal((await invoke(page,'ListTabs')).length,before.length);
  check('20 native RPC opens reuse one draft without creating a runtime tab');
  let draft = drafts[0];
  // Exercise the native clipboard service, keeping the explicit draft target.
  await installClipboardFixture(shell);
  let attachment;
  try { attachment = await invoke(page,'SaveClipboardImageForTarget',[{kind:'draft',draftId:draft.id}]); }
  finally { await restoreClipboard(shell); }
  assert.ok(attachment);
  const content = { text:'native restart fixture', attachments:[{path:attachment}], invocations:[], workspaceRefs:[], sessionRefs:[], selectedTextRefs:[], pastedBlocks:[], openPastedLabels:[] };
  const saved = await invoke(page,'SaveSessionDraft',[{draftId:draft.id,revision:draft.revision,contentJson:JSON.stringify(content),settings:draft.settings}]);
  assert.equal(saved.outcome,'saved');
  assert.ok(saved.draft.snapshotDigest);
  draft = saved.draft;
  await invoke(page,'SetSessionDraftRestoreTarget',[draft.id]);
  check('native clipboard attachment is saved to the captured workspace with a confirmed digest');
  await page.evaluate(() => window.__reasonixFlushSessionDraft?.());
  await shell.close();
  shell = await launch();
  page = await ready(shell);
  const restored = await invoke(page,'GetSessionDraftState',[draft.id]);
  assert.equal(restored.draft.id,draft.id);
  assert.equal(JSON.parse(restored.draft.contentJson).text,content.text);
  assert.equal(JSON.parse(restored.draft.contentJson).attachments[0].path,attachment);
  const preview = await invoke(page,'AttachmentDataURLForTarget',[{kind:'draft',draftId:draft.id},attachment]);
  assert.match(preview,/^data:image\//);
  assert.equal((await invoke(page,'ListTabs')).length,before.length);
  check('normal Electron restart restores text and native attachment without creating a session');
  const composer = page.locator('textarea.composer__input:not([aria-hidden=true])');
  await composer.waitFor({state:'visible'});
  const droppedPath = join(home, 'native-drop-fixture.txt');
  writeFileSync(droppedPath, 'disposable native file drop fixture');
  const bounds = await page.locator('[data-native-drop-target]').first().boundingBox();
  assert.ok(bounds);
  const cdp = await page.context().newCDPSession(page);
  const drag = {x:bounds.x+bounds.width/2,y:bounds.y+bounds.height/2,data:{items:[],files:[droppedPath],dragOperationsMask:1}};
  await cdp.send('Input.dispatchDragEvent',{type:'dragEnter',...drag});
  await cdp.send('Input.dispatchDragEvent',{type:'dragOver',...drag});
  await cdp.send('Input.dispatchDragEvent',{type:'drop',...drag});
  await page.waitForFunction(async id => {
    const state = await window.reasonixDesktop.invoke('GetSessionDraftState',[id]);
    const content = JSON.parse(state.draft.contentJson);
    return content.attachments.length > 1 || content.workspaceRefs.length > 0;
  }, draft.id);
  await cdp.detach();
  check('native file drop crosses the preload boundary and persists in its draft');
  const beforePaste = JSON.parse((await invoke(page,'GetSessionDraftState',[draft.id])).draft.contentJson).attachments.length;
  await installClipboardFixture(shell);
  try {
    await composer.focus();
    await page.keyboard.press('Meta+v');
    await page.waitForFunction(async ({id,count}) => {
      const state = await window.reasonixDesktop.invoke('GetSessionDraftState',[id]);
      return JSON.parse(state.draft.contentJson).attachments.length > count;
    }, {id:draft.id,count:beforePaste});
  } finally { await restoreClipboard(shell); }
  check('native keyboard paste persists through the Composer task owner');
  await composer.fill('native close flushes the latest editor version');
  // Close immediately, without waiting for the debounce or manually flushing.
  await shell.close();
  shell = await launch();
  page = await ready(shell);
  draft = (await invoke(page,'GetSessionDraftState',[draft.id])).draft;
  assert.equal(JSON.parse(draft.contentJson).text,'native close flushes the latest editor version');
  check('native renderer edit survives immediate normal close through the shutdown barrier');
  // Kill the actual Electron process after confirmed persistence. The isolated
  // service must recover through its ordinary parent/pipe shutdown path.
  const stopped = shell.waitForEvent('close');
  shell.process().kill('SIGKILL');
  await stopped;
  shell = await launch();
  page = await ready(shell);
  const recovered = await invoke(page,'GetSessionDraftState',[draft.id]);
  assert.equal(recovered.draft.contentJson,draft.contentJson);
  check('confirmed data survives Electron SIGKILL and service restart without replay');
} finally {
  if(shell) await shell.close().catch(()=>{});
  writeFileSync(join(home,'draft-smoke-results.json'),JSON.stringify(report,null,2));
  console.log(`Evidence: ${join(home,'draft-smoke-results.json')}`);
}
