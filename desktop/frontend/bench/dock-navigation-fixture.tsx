import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { WorkspaceDockRegion } from "../src/app-shell/WorkspaceDockRegion";
import { PresentedFiles } from "../src/components/PresentedFiles";
import { LocaleProvider } from "../src/lib/i18n";
import { useActivityBarStore } from "../src/store/activityBar";
import { useRemoteStore } from "../src/store/remote";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import "../src/styles.css";

const file = (path: string) => ({ path, body: `NAVIGATION CONTENT ${path}`, size: 40, binary: false, truncated: false, mtimeUnix: 1 });
let finishSave = () => {};
installDesktopHostStub({
  ListDirForTab: async () => [], SearchFileRefsForTab: async () => [],
  WorkspaceChanges: async () => ({ files: [], gitAvailable: true }), WorkspaceGitHistory: async () => [],
  ResolveWorkspacePathForTab: async (_tab: string, path: string) => path.startsWith("/") ? path : `/fixture/${path}`,
  ResolvePresentedPathForTab: async (_tab: string, _tool: string, path: string) => path.startsWith("/") ? path : `/fixture/${path}`,
  ResolveRemoteWorkspacePathForTab: async (_tab: string, _host: string, _tool: string, path: string) => path,
  ResolveRemotePresentedPathForTab: async (_tab: string, _host: string, _tool: string, path: string) => path,
  ReadFileForTab: async (_tab: string, path: string) => file(path),
  ReadPresentedFileForTab: async (_tab: string, _tool: string, path: string) => file(path),
  ReadPresentedFileSourceForTab: async (_tab: string, _tool: string, path: string) => file(path),
  ListRemoteDir: async () => ["a.txt", "b.txt"].map(name => ({ name, path: name, isDir: false, size: 40 })),
  ReadRemoteFile: async (_host: string, path: string) => file(path),
  WriteRemoteFile: () => new Promise(resolve => { finishSave = () => resolve({ conflict: false, newMtimeUnix: 2 }); }),
});
useActivityBarStore.setState({ workspaceRoot: "/fixture", tabs: [], activeTabId: null });
useRemoteStore.setState({ statuses: { remote: { state: "connected" } } });

function Fixture() {
  const [hostId, setHostId] = useState("local");
  return <LocaleProvider>
    <button id="switch-host" onClick={() => setHostId(value => value === "local" ? "remote" : "local")}>Switch host</button>
    <button id="finish-save" onClick={() => finishSave()}>Finish save</button>
    <PresentedFiles tabId={hostId} hostId={hostId} files={[{ path: `${hostId}.txt`, toolCallId: "present", description: "Navigation regression" }]} />
    <WorkspaceDockRegion visible overlay={false} mode="files" creation={false} showContext={false}
      t={key => key} onPickEntry={() => {}} remote={{ onClose: () => {} }} context={{} as never}
      workspace={{ open: true, tabId: hostId, cwd: "/fixture", maximized: false, onClose: () => {}, onToggleMaximized: () => {} }}
      workspaceRoot="/fixture" workspaceKey="fixture" />
  </LocaleProvider>;
}
createRoot(document.getElementById("root")!).render(<Fixture />);
