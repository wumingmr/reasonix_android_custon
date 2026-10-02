import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { installDom } from "./composerInboxHarness";
import { RichComposerInput, type RichComposerInputHandle } from "../components/RichComposerInput";
import { LocaleProvider } from "../lib/i18n";
import type { ComposerInvocation } from "../lib/invocationDisplay";

const dom=installDom();
const root=createRoot(document.getElementById("root")!);
const handle=React.createRef<RichComposerInputHandle>();
const command={name:"restored-skill",kind:"skill" as const,description:"skill"};
let result:ComposerInvocation[]=[];
const noop=()=>{};
try {
 await act(async()=>root.render(<LocaleProvider><RichComposerInput ref={handle} text=" /" invocations={[{id:"invocation-1",offset:0,command}]} placeholder="" disabled={false}
  onSelectionChange={noop} onKeyDown={noop} onContextMenu={noop} onPaste={noop} onCompositionStart={noop} onCompositionEnd={noop}
  onChange={(_text,items)=>{result=items;}} /></LocaleProvider>));
 await act(async()=>handle.current!.insertInvocation({...command,name:"new-skill"},{from:1,to:2,query:""}));
 assert.equal(result.length,2);
 assert.equal(new Set(result.map(item=>item.id)).size,2,"restored IDs cannot collide with this renderer's counter");
 assert.equal(result[0].id,"invocation-1","preserve original identity");
 console.log("PASS restored rich composer invocation identity");
} finally {await act(async()=>root.unmount());dom.window.close();}
