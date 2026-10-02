// Fixture for bench/chat-file-reference.mjs: one assistant answer that names a
// file in inline code and shows an SVG fence, wired to the same providers the
// transcript uses.
import { Fragment, useSyncExternalStore } from "react";
import { createRoot } from "react-dom/client";
import { LocaleProvider } from "../src/lib/i18n";
import { parseMarkdownToBlocks } from "../src/lib/markdownPipeline";
import { hastBlockToJsx } from "../src/lib/hastJsx";
import { createComponents } from "../src/components/markdownComponents";
import { ChatFileScopeProvider, ChatFileTurnProvider, useChatFileCandidateReport } from "../src/components/ChatFileLinkContext";
import { fileNavigationOwner } from "../src/lib/fileNavigationCommands";
import { useActivityBarStore } from "../src/store/activityBar";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import type { AppBindings } from "../src/lib/bridge";

// Deliberately namespace-free: the model's usual spelling, and the one
// that used to fail to render as an image.
const SVG = `<svg viewBox="0 0 20 10">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#f60"/></linearGradient></defs>
  <rect width="20" height="10" fill="url(#g)"/>
  <text x="2" y="7">preview</text>
</svg>`;

// The sanitizer runs in the Go host, which this browser fixture cannot call.
// SANITIZED is therefore the host's real output for SVG above, captured byte
// for byte and pinned by TestMarkdownSVGBenchFixtureMatchesTheSanitizer: if the
// host stops producing these bytes that Go test fails and this fixture must be
// updated. Rendering a hand-written stand-in instead is exactly how a preview
// that never loads in a browser still passes.
const SANITIZED = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 10">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#f60"></stop></linearGradient></defs>
  <rect width="20" height="10" fill="url(#g)"></rect>
  <text x="2" y="7">preview</text>
</svg>`;

installDesktopHostStub(({
  main: {
    App: {
      ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => ({
        turnKey,
        references: candidates.map(candidate => candidate.path === "/repo/out/diagram.svg"
          ? { key: candidate.key, path: candidate.path, status: "resolved", displayPath: "out/diagram.svg", kind: "image", actions: ["preview", "reveal-tree", "copy-path", "save-copy", "source", "open-native", "reveal-native"] }
          : { key: candidate.key, path: candidate.path, status: "unavailable", actions: [], reason: "not-found" }),
      }),
      SanitizeMarkdownSVG: async (content: string) => ({ ok: true, svg: content.includes("linearGradient id=\"g\"") && content.includes("/>") ? SANITIZED : content }),
      ResolveReferencePathForTab: async (_tabId: string, path: string) => path.startsWith("/") ? path : `/repo/${path}`,
      ReadReferenceFileForTab: async (_tabId: string, path: string) => ({ path, body: "PREVIEW BODY", size: 12, truncated: false, binary: false }),
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

const blocks = parseMarkdownToBlocks(`Wrote \`/repo/out/diagram.svg\` and used \`/repo/out/missing.svg\`.\n\n\`\`\`svg\n${SVG}\n\`\`\`\n`);
const components = createComponents(false);

/** Surfaces the running owner's committed navigation result. */
function RequestProbe() {
  const owner = fileNavigationOwner();
  const snapshot = useSyncExternalStore(owner.subscribe, () => {
    const dock = useActivityBarStore.getState().tabs.find(tab => tab.type === "file");
    return dock ? owner.getSnapshot(dock.id) : null;
  });
  const navigation = snapshot?.navigation;
  return <pre id="request" hidden>{navigation ? JSON.stringify({
    source: navigation.resource.access.source,
    path: navigation.resource.requestedPath,
    action: navigation.params.action,
  }) : ""}</pre>;
}

function Reporter() {
  useChatFileCandidateReport(blocks, 1);
  return null;
}

createRoot(document.getElementById("root")!).render(
  <LocaleProvider>
    <ChatFileScopeProvider scopeKey="bench" tabId="tab-bench">
      <ChatFileTurnProvider turnKey="turn-bench" factsVersion={1} presentedFiles={[]} modifiedFiles={[]} tabId="tab-bench">
        <Reporter />
        <div className="md">{blocks.map(block => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}</div>
      </ChatFileTurnProvider>
    </ChatFileScopeProvider>
    <RequestProbe />
  </LocaleProvider>,
);
