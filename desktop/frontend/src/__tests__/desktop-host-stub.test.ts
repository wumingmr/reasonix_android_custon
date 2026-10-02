import assert from "node:assert/strict";
import { installDesktopHostStub } from "./desktopHostStub";
import { desktopHost } from "../lib/desktopHost";

Object.defineProperty(globalThis, "window", { configurable: true, value: {} });
const commands: { Version: () => Promise<string>; TranscriptSnapshotForTab?: () => Promise<unknown> } = { Version: async () => "test", TranscriptSnapshotForTab: undefined };
const stub = installDesktopHostStub(commands);
assert.equal(typeof desktopHost().app?.Version, "function");
assert.equal(desktopHost().app?.TranscriptSnapshotForTab, undefined, "an unimplemented optional method must not advertise protocol support");
commands.TranscriptSnapshotForTab = async () => ({});
assert.equal(typeof desktopHost().app?.TranscriptSnapshotForTab, "function", "a newly installed method is discovered without reinstalling the host");
delete commands.TranscriptSnapshotForTab;
assert.equal(desktopHost().app?.TranscriptSnapshotForTab, undefined);
stub.uninstall();
console.log("desktop host stub advertises only callable commands");
