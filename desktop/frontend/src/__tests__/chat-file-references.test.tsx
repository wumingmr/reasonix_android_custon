// Run: tsx src/__tests__/chat-file-references.test.tsx
//
// Answer-named files end to end: extraction from the parsed Markdown AST, the
// host round trip that verifies them, and the rendering rules that keep an
// unverified path as ordinary text.

import { JSDOM } from "jsdom";
import React, { Fragment, act } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createRoot } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import type { ChatFileReference, ChatFileReferenceResult } from "../generated/desktopContract.generated";
import { chatFileCandidates } from "../lib/chatFileCandidates";
import { looksLikeSvgDocument, svgAspectRatio } from "../lib/svgDocument";
import { linkifyLocalPaths } from "../lib/localPathLinks";
import { parseMarkdownToBlocks } from "../lib/markdownPipeline";
import { hastBlockToJsx } from "../lib/hastJsx";
import { createComponents } from "../components/markdownComponents";
import { LocaleProvider } from "../lib/i18n";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) { process.stdout.write(`  PASS  ${label}\n`); passed += 1; }
  else { process.stdout.write(`  FAIL  ${label}\n`); failed += 1; }
}

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;

console.log("\nchat file references");

// ── Candidate extraction runs on the parsed AST ─────────────────────────────
function candidatesOf(text: string) {
  return chatFileCandidates(parseMarkdownToBlocks(text));
}

ok(candidatesOf("See `/repo/out/图 (1).svg` now.").some(c => c.path === "/repo/out/图 (1).svg"),
  "an inline code span keeps its spaces and CJK intact");
ok(candidatesOf("Wrote `out/diagram.svg` for you.").some(c => c.path === "out/diagram.svg"),
  "a relative inline-code path with a directory is a candidate");
ok(candidatesOf("Wrote `diagram.svg` for you.").length === 0,
  "a bare filename is left to the turn's known file set, never resolved by guess");
ok(candidatesOf("Run `npm run build` first.").length === 0, "an ordinary command is not a candidate");
ok(candidatesOf("Answer: `/api/v1/users` and `/usr/bin/env`.").length === 2,
  "an inline code span is its own delimiter, so the host decides what it names");
ok(candidatesOf("```svg\n<svg xmlns=\"http://www.w3.org/2000/svg\"><rect/></svg>\n```").length === 0,
  "a fenced block's own text is never scanned");
ok(candidatesOf("<svg width=\"1\" onload=\"/tmp/x.svg\">").length === 0, "raw markup is not scanned");
ok(candidatesOf("[report](/tmp/report.pdf)").some(c => c.path === "/tmp/report.pdf"),
  "an explicit Markdown file link is a candidate");
ok(candidatesOf("[docs](https://example.com/a.md)").length === 0, "a remote link is not a candidate");
const deduped = candidatesOf("`/a/b/c.svg` and again `/a/b/./c.svg`");
ok(deduped.length === 1, "the same file spelled twice is one candidate");
ok(candidatesOf("`out\\win\\nested.svg`").some(c => c.path === "out\\win\\nested.svg"),
  "a Windows-style relative spelling survives extraction");

// ── Scanned POSIX prose paths are tagged for the renderer ───────────────────
const scanned = linkifyLocalPaths("Saved to /Users/me/我的 项目 is not right, but /tmp/out.svg is.");
const scannedPath = scanned.find(segment => segment.path);
ok(scannedPath?.kind === "posix" && scannedPath.path === "/tmp/out.svg",
  "a clearly delimited POSIX path in prose is recognised as a scanned path");
ok(linkifyLocalPaths("See https://x.test/a.png").every(segment => segment.path === undefined),
  "a URL is not scanned as a local path");

// ── SVG recognition ─────────────────────────────────────────────────────────
ok(looksLikeSvgDocument(`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`), "a bare svg root is recognised");
ok(looksLikeSvgDocument(`<?xml version="1.0"?>\n<!-- c -->\n<svg/>`), "a prolog and comment are tolerated");
const adversarialCommentPrefix = `<!--${"--><!--".repeat(2_000)}x`;
const adversarialStarted = performance.now();
ok(!looksLikeSvgDocument(adversarialCommentPrefix), "an unterminated adversarial comment is refused");
ok(performance.now() - adversarialStarted < 100, "SVG prefix recognition stays linear");
ok(!looksLikeSvgDocument("<html><body>x</body></html>"), "mixed HTML is not an SVG document");
ok(!looksLikeSvgDocument("just text"), "ordinary text is not an SVG document");
ok(svgAspectRatio(`<svg viewBox="0 0 200 100"></svg>`) === 2, "viewBox supplies the aspect ratio");
ok(svgAspectRatio(`<svg width="30" height="10"></svg>`) === 3, "width and height supply the aspect ratio");
ok(svgAspectRatio(`<svg viewBox="0 0 0 0"></svg>`) === undefined, "a degenerate viewBox has no ratio");

// ── Host round trip and rendering ───────────────────────────────────────────
const referenceCalls: Array<{ turnKey: string; paths: string[] }> = [];
const desktopStub = installDesktopHostStub(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>): Promise<ChatFileReferenceResult> => {
        referenceCalls.push({ turnKey, paths: candidates.map(candidate => candidate.path) });
        return {
          turnKey,
          references: candidates.map(candidate => candidate.path === "/repo/out/diagram.svg"
            ? { key: candidate.key, path: candidate.path, status: "resolved", displayPath: "out/diagram.svg", kind: "image", actions: ["preview", "reveal-tree", "copy-path", "save-copy", "source", "open-native", "reveal-native"] }
            : { key: candidate.key, path: candidate.path, status: "unavailable", actions: [], reason: "not-found" }),
        };
      },
      SanitizeMarkdownSVG: async (content: string) => {
        return content.includes("<script")
          ? { ok: false, reason: "invalid" }
          : { ok: true, svg: content };
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

const markdownBlocks = parseMarkdownToBlocks("Wrote `/repo/out/diagram.svg` and claimed `/repo/out/missing.svg`.\n\nSaved at /tmp/scanned-only.svg as well.");
const components = createComponents(false);

const { ChatFileScopeProvider, ChatFileTurnProvider, useChatFileCandidateReport } = await import("../components/ChatFileLinkContext");

/** Drives the production reporting hook, which MarkdownHistory calls after a parse. */
function Reporter({ blocks }: { blocks: readonly { children: readonly unknown[] }[] }) {
  useChatFileCandidateReport(blocks, 1);
  return null;
}
const MarkdownSvgBlock = (await import("../components/MarkdownSvgBlock")).default;

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);

function paint(node: React.ReactNode) {
  return act(async () => { root.render(<LocaleProvider>{node}</LocaleProvider>); });
}

await paint(
  <ChatFileScopeProvider scopeKey="session-a" tabId="tab-a">
    <ChatFileTurnProvider turnKey="turn-1" factsVersion={1} presentedFiles={[]} modifiedFiles={[]} tabId="tab-a">
      <Reporter blocks={markdownBlocks} />
      {markdownBlocks.map(block => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}
    </ChatFileTurnProvider>
  </ChatFileScopeProvider>,
);
await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });

ok(referenceCalls.length > 0, "committed blocks ask the host to verify their candidates");
ok(referenceCalls.every(call => call.turnKey === "turn-1"), "the host call carries the answer's turn");
ok(referenceCalls.flatMap(call => call.paths).includes("/repo/out/diagram.svg"), "the inline-code path was submitted");
ok(referenceCalls.flatMap(call => call.paths).every(path => path.startsWith("/") || path.includes("/")),
  "only path-shaped candidates reach the host");

const body = () => rootEl.textContent ?? "";
ok(body().includes("/repo/out/diagram.svg"), "the answer text is preserved verbatim");
ok(document.querySelectorAll("button.md-code--presented-file").length === 1,
  "a verified reference is clickable");
ok(document.querySelector("button.md-code--presented-file")?.getAttribute("title") === "out/diagram.svg",
  "the clickable reference points at the host's display path");
ok(body().includes("/repo/out/missing.svg") && document.querySelectorAll("button.md-code--presented-file").length === 1,
  "an unverified path stays ordinary text");
ok(document.querySelector("span.md-rich-link__plain") !== null,
  "a scanned prose path stays inert until the host confirms it");

// ── The SVG code block ──────────────────────────────────────────────────────
const svgSource = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 5"><linearGradient id="g"/><text x="1" y="1">hi</text></svg>`;
const svgMarkup = (value: string) => renderToStaticMarkup(<LocaleProvider><MarkdownSvgBlock value={value} /></LocaleProvider>);

const pending = svgMarkup(svgSource);
ok(pending.includes("md-svg"), "an SVG block renders its own frame");
ok(pending.includes("md-svg__note") === false, "a pending sanitize does not claim the source is unshowable");
ok(pending.includes("linearGradient"), "the source is available while the preview is pending");

const svgBlocks = parseMarkdownToBlocks("```svg\n" + svgSource + "\n```");
ok(svgBlocks.length === 1, "an svg fence parses as one block");
await act(async () => {
  root.render(<LocaleProvider>
    {svgBlocks.map(block => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}
  </LocaleProvider>);
});
await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
ok(document.querySelector(".md-svg") !== null, "an svg fence renders the preview block, not a plain code block");
ok(document.querySelector(".md-svg__preview img") !== null, "the sanitized SVG becomes an image source, never injected markup");
ok(/^(blob:|data:image\/svg\+xml)/.test(document.querySelector(".md-svg__preview img")?.getAttribute("src") ?? ""),
  "the preview loads the host's sanitized bytes as an image source");
ok(document.querySelector(".md-svg__note") === null, "a previewable SVG shows no fallback note");
ok(Boolean(document.querySelector(".md-svg__copy")), "the block keeps a copy action for the original source");

const codeFence = parseMarkdownToBlocks("```html\n<div>not an svg</div>\n```");
ok(codeFence.length === 1, "an html fence still parses");
await act(async () => {
  root.render(<LocaleProvider>
    {codeFence.map(block => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}
  </LocaleProvider>);
});
ok(document.querySelector(".md-svg") === null && document.querySelector(".code-block") !== null,
  "an html fence that is not a single SVG root keeps the ordinary code block");

const RefusedBlock = (await import("../components/MarkdownSvgBlock")).default;
const refused = renderToStaticMarkup(<LocaleProvider><RefusedBlock value={`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`} /></LocaleProvider>);
ok(refused.includes("md-svg"), "a refused SVG still renders inside the block frame");
ok(refused.includes("script"), "the refused SVG keeps its source visible");

// ── The session owner batches, dedupes, and rejects late work ───────────────
const batchCalls: string[][] = [];
let gate: (() => void) | null = null;
desktopStub.replaceCommands(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => {
        batchCalls.push(candidates.map(candidate => candidate.path));
        if (gate) await new Promise<void>(resolve => { const open = gate!; gate = () => { open(); resolve(); }; });
        return {
          turnKey,
          references: candidates.map(candidate => ({ key: candidate.key, path: candidate.path, status: "resolved", displayPath: candidate.path, actions: ["preview"] })),
        };
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

const { ChatFileReferenceStore, CHAT_FILE_REFERENCE_BATCH_LIMIT } = await import("../lib/chatFileReferences");
const flush = async () => { for (let i = 0; i < 6; i += 1) await Promise.resolve(); };

const store = new ChatFileReferenceStore("tab-b");
const many = Array.from({ length: CHAT_FILE_REFERENCE_BATCH_LIMIT + 5 }, (_, index) => ({ key: `k${index}`, path: `/tmp/f${index}.svg` }));
store.report("turn-a", 1, many);
await flush();
// Both host batches settle in microtasks; yield once to their completion
// instead of assuming a fixed number of Promise continuations is sufficient.
await new Promise<void>(resolve => setImmediate(resolve));
ok(batchCalls.length === 2, "a set larger than the batch limit is split");
ok(batchCalls.every(batch => batch.length <= CHAT_FILE_REFERENCE_BATCH_LIMIT), "no batch exceeds the host contract");
ok(store.getTurnSnapshot("turn-a").size === many.length, "every candidate keeps a cached verdict");

batchCalls.length = 0;
store.report("turn-a", 1, many);
await flush();
ok(batchCalls.length === 0, "re-reporting the same candidates does not ask the host again");

const failedStore = new ChatFileReferenceStore("tab-c");
desktopStub.replaceCommands(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => {
        batchCalls.push(candidates.map(candidate => candidate.path));
        return { turnKey, references: candidates.map(candidate => ({ key: candidate.key, path: candidate.path, status: "unavailable", actions: [], reason: "not-found" })) };
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);
batchCalls.length = 0;
failedStore.report("turn-a", 1, [{ key: "k", path: "/tmp/late.svg" }]);
await flush();
ok(batchCalls.length === 1, "a missing file is asked about once");
failedStore.report("turn-a", 1, [{ key: "k", path: "/tmp/late.svg" }]);
await flush();
ok(batchCalls.length === 1, "a cached failure is not retried on every render");
failedStore.report("turn-a", 2, [{ key: "k", path: "/tmp/late.svg" }]);
await flush();
ok(batchCalls.length === 2, "new file facts make an earlier failure worth asking about again");

const lateStore = new ChatFileReferenceStore("tab-d");
gate = () => {};
desktopStub.replaceCommands(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => {
        await new Promise<void>(resolve => { gate = () => resolve(); });
        return { turnKey, references: candidates.map(candidate => ({ key: candidate.key, path: candidate.path, status: "resolved", displayPath: candidate.path, actions: ["preview"] })) };
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);
lateStore.report("turn-a", 1, [{ key: "k", path: "/tmp/inflight.svg" }]);
await flush();
lateStore.dispose();
gate?.();
await flush();
ok(lateStore.getTurnSnapshot("turn-a").size === 0, "a reply from a replaced session never reaches the new one");

// ── A remote transcript never resolves through the local host ───────────────
let remoteCalls = 0;
desktopStub.replaceCommands(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => {
        remoteCalls += 1;
        return { turnKey, references: candidates.map(candidate => ({ key: candidate.key, path: candidate.path, status: "resolved", displayPath: candidate.path, actions: ["preview"] })) };
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

const remoteBlocks = parseMarkdownToBlocks("Wrote `/srv/app/out/remote.svg` on the server.");
await act(async () => {
  root.render(<LocaleProvider>
    <ChatFileScopeProvider scopeKey="session-r" tabId="tab-r" hostId="remote-test">
      <ChatFileTurnProvider turnKey="turn-r" factsVersion={0} presentedFiles={[]} modifiedFiles={[]} tabId="tab-r" hostId="remote-test">
        <Reporter blocks={remoteBlocks} />
        {remoteBlocks.map(block => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}
      </ChatFileTurnProvider>
    </ChatFileScopeProvider>
  </LocaleProvider>);
});
await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
ok(remoteCalls === 0, "a remote answer is never verified against the local filesystem");
ok(document.querySelectorAll("button.md-code--presented-file").length === 0,
  "an unverified remote path stays ordinary text rather than opening locally");
ok((rootEl.textContent ?? "").includes("/srv/app/out/remote.svg"), "the remote answer text is untouched");

// ── A StrictMode mount replay must not kill the session store ───────────────
let strictCalls = 0;
desktopStub.replaceCommands(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => {
        strictCalls += 1;
        return { turnKey, references: candidates.map(candidate => ({ key: candidate.key, path: candidate.path, status: "resolved", displayPath: candidate.path, actions: ["preview"] })) };
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);
const strictBlocks = parseMarkdownToBlocks("Wrote `/repo/out/strict.svg` here.");
await act(async () => {
  root.render(<React.StrictMode>
    <LocaleProvider>
      <ChatFileScopeProvider scopeKey="session-s" tabId="tab-s">
        <ChatFileTurnProvider turnKey="turn-s" factsVersion={1} presentedFiles={[]} modifiedFiles={[]} tabId="tab-s">
          <Reporter blocks={strictBlocks} />
          {strictBlocks.map(block => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}
        </ChatFileTurnProvider>
      </ChatFileScopeProvider>
    </LocaleProvider>
  </React.StrictMode>);
});
await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
ok(strictCalls > 0, "a StrictMode mount replay does not leave the session with a disposed store");

// ── Teardown ────────────────────────────────────────────────────────────────
await act(async () => { root.unmount(); });
desktopStub.uninstall();
dom.window.close();

process.stdout.write(`\n${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
