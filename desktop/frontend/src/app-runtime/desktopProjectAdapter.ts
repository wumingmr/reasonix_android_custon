import { app } from "../lib/bridge";
import type { SessionSelector } from "../generated/desktopContract.generated";

export const desktopProjectAdapter = {
  renameLocal: (selector: SessionSelector, title: string) => app.RenameSessionTarget(selector, title),
  listRemote: (host: string, workspace: string) => app.RemoteProjectSessions(host, workspace),
  renameRemote: (host: string, workspace: string, name: string, title: string) => app.RenameRemoteProjectSession(host, workspace, name, title),
};
