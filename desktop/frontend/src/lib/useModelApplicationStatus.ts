import { useEffect, useState } from "react";
import { app, onRuntimeRebuilt } from "./bridge";
import { runtimeStateStore } from "./runtimeStateStore";
import type { ModelSettingsResult } from "./modelSettingsTypes";

// Lifecycle events request a coalesced read of backend-owned state.
export function useModelApplicationStatus(tabId?: string) {
  const [target,setTarget]=useState<ModelSettingsResult["targets"][number]>();
  useEffect(()=>{
    setTarget(undefined);
    if(!tabId || !app.GetModelSettingsApplication) return;
    let active=true, reading=false, pending=false;
    const refresh=async()=>{
      if(reading){pending=true;return;}
      reading=true;
      do {
        pending=false;
        try {
          const result=await app.GetModelSettingsApplication();
          if(active) setTarget(result.targets.find(item=>item.tabId===tabId));
        } catch { /* Keep confirmed status on transport failure. */ }
      } while(active && pending);
      reading=false;
    };
    const notify=()=>{void refresh();};
    const rebuilt=onRuntimeRebuilt(id=>{if(!id || id===tabId) notify();});
    const runtime=runtimeStateStore.subscribe(notify);
    window.addEventListener("reasonix:model-catalog-changed",notify);
    window.addEventListener("focus",notify);
    notify();
    return ()=>{active=false;rebuilt();runtime();window.removeEventListener("reasonix:model-catalog-changed",notify);window.removeEventListener("focus",notify);};
  },[tabId]);
  return target;
}
