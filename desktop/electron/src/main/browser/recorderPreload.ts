import { ipcRenderer } from "electron";
ipcRenderer.once("reasonix-recorder-port", event => {
  window.postMessage("reasonix-recorder-port", "*", event.ports);
});
