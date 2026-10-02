import assert from "node:assert/strict";
import { classifyTool, shellDisplayName, type ToolItem } from "../lib/chatToolPresentation";
import { deriveTurnFiles, fileIdentity } from "../lib/turnFiles";
import { fileResourceCapabilities, type FileResourceRef } from "../lib/fileResource";
import { subjectOf, summarize } from "../lib/tools";
import { historyMessagesToItems } from "../lib/historyItems";

const tool = (overrides: Partial<ToolItem>): ToolItem => ({
  kind: "tool", id: "call", name: "unknown", args: "{}", readOnly: true, status: "done", ...overrides,
});

assert.equal(classifyTool(tool({ name: "bash", isShell: true, execution: { shell: "pwsh" } })), "shell");
for (const name of ["pwsh", "PWSH", "PowerShell", "powershell", "bash", "BASH", "shell"]) {
  const args = JSON.stringify({ command: "Write-Output example" });
  assert.equal(classifyTool(tool({ name })), "shell", `live ${name} has a terminal renderer before metadata arrives`);
  assert.equal(subjectOf(name, args), "Write-Output example");
  assert.equal(summarize(name, args, "one\ntwo\n"), summarize("bash", args, "one\ntwo\n"));
  const history = historyMessagesToItems([
    { role: "assistant", content: "", toolCalls: [{ id: "call", name, arguments: args }] },
    { role: "tool", content: "example", toolName: name, toolCallId: "call" },
  ], "test");
  const call = history.items.find(item => item.kind === "tool");
  assert.ok(call?.kind === "tool" && call.isShell, `restored ${name} remains a shell`);
}
assert.equal(shellDisplayName(tool({ name: "pwsh" })), "PowerShell");
assert.equal(classifyTool(tool({ name: "mcp_pwsh" })), "tool");
assert.equal(shellDisplayName(tool({ name: "bash", isShell: true, execution: { shell: "pwsh" } })), "PowerShell");
assert.equal(shellDisplayName(tool({ name: "bash", isShell: true, execution: { shell: "zsh" } })), "Zsh");
assert.equal(shellDisplayName(tool({ name: "bash", isShell: true })), "Terminal");
assert.equal(classifyTool(tool({ name: "write_file" })), "file");
assert.equal(classifyTool(tool({ name: "plugin_write_report", capabilityId: "mcp-tool:plugin:write" })), "tool",
  "untrusted names containing write do not acquire the native file renderer");

const calls: ToolItem[] = [
  tool({ id: "write", name: "write_file", args: '{"path":"./docs/guide.md","content":"hi"}', readOnly: false }),
  tool({ id: "edit", name: "edit_file", args: '{"path":"docs/./guide.md"}', readOnly: false }),
  tool({ id: "noop", name: "write_file", args: '{"path":"noop.txt"}', output: "noop.txt already contains the exact content; no changes made", readOnly: false }),
  tool({ id: "failed", name: "edit_file", args: '{"path":"failed.txt"}', error: "not found", status: "error", readOnly: false }),
  tool({ id: "shell", name: "bash", args: '{"command":"touch guessed.txt"}', readOnly: false, isShell: true }),
  tool({ id: "read", name: "read_file", args: '{"path":"read.txt"}', readOnly: true }),
  tool({ id: "move", name: "move_file", args: '{"source_path":"old.txt","destination_path":"new.txt"}', readOnly: false }),
];
assert.deepEqual(deriveTurnFiles(calls), [
  { path: "./docs/guide.md", toolCallId: "edit", operation: "modified" },
  { path: "new.txt", toolCallId: "move", operation: "written" },
]);
assert.equal(fileIdentity("a/./b/../c.txt"), "a/c.txt");
// Capabilities answer for a caller-shaped reference, and read only its host and
// name: the origin and the tool call never change what the row may offer.
const remoteReportRef: FileResourceRef = { source: "workspace", hostId: "remote-a", tabId: "tab", toolCallId: "write", path: "report.md" };
assert.deepEqual(fileResourceCapabilities(remoteReportRef), {
  preview: true, source: true, browser: false, revealTree: true, copyPath: true, openNative: false, revealNative: false, saveCopy: true,
});
const localPresentedRef: FileResourceRef = { source: "presented", hostId: "local", tabId: "tab", toolCallId: "present", path: "app.html" };
assert.equal(fileResourceCapabilities(localPresentedRef).browser, true);

console.log("chat tool presentation: trusted renderer matching, shell labels and file facts passed");

const { historyToolStatus } = await import("../lib/historyToolStatus");
const { toolPresentation } = await import("../lib/chatToolPresentation");
assert.equal(historyToolStatus(undefined), "unknown", "unloaded results must not become stopped");
assert.equal(historyToolStatus(undefined, { id: "call", name: "bash", arguments: "{}", resultObservation: { state: "completed", messageId: "result", version: 1 } }), "done");
assert.equal(toolPresentation(tool({ status: "unknown", resultMissing: true })).state, "unknown");
assert.equal(historyToolStatus({ role: "tool", content: "", execution: { state: "cancelled" } }), "stopped");
for (const state of ["running", "failed", "cancelled", "completed"]) {
  const call = { id: "poll", name: "job_output", arguments: "{}", resultObservation: { state: "completed" as const, messageId: "poll-result", version: 1 } };
  const result = { role: "tool", toolName: "job_output", toolCallId: "poll", content: `[status: ${state}]`, execution: { state } };
  const projected = historyMessagesToItems([{ role: "assistant", content: "", toolCalls: [call] }, result], "poll");
  const item = projected.items.find((item): item is ToolItem => item.kind === "tool")!;
  assert.equal(item.status, "done", "the output read completed independently of the observed job state");
  assert.equal(toolPresentation(item).dot, "done", "job state metadata must not override the output-read receipt");
  assert.equal(toolPresentation({ ...item, status: "running" }).dot, "ongoing", "an in-flight output read still has its own running state");
}
for (const name of ["pwsh", "bash"]) {
  const launch = tool({ name, execution: { state: "background_started" } });
  assert.deepEqual(toolPresentation(launch), { state: "done", dot: "done", label: "chat.background" },
    "a background launch receipt cannot remain a perpetual running indicator");
  assert.equal(toolPresentation(tool({ name, status: "running" })).dot, "ongoing",
    "a foreground call without a result still reports running");
}
