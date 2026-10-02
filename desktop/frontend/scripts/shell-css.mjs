// --reasonix-draggable marks OS drag regions. Electron rewrites chrome
// drag/no-drag to -webkit-app-region. Transcript content must not become
// native no-drag or Chromium punches a hole in the titlebar (#10105).

const DRAG_VALUE = /--reasonix-draggable(\s*:\s*)(drag|no-drag)/g;
const APP_REGION_DECL = /\s*-webkit-app-region\s*:\s*(?:no-)?drag;?/g;
const CHROME_NO_DRAG_SELECTOR =
  /(^|[,{\s])\.(?:topicbar(?:__|--)[\w-]*|topbar(?:__|--)[\w-]*|topicbar|topbar|sidebar[\w-]*|windows-window-control[\w-]*|management-screen[\w-]*|heartbeat[\w-]*|app-chrome[\w-]*|tabbar[\w-]*|chip|modal-close-button|[\w-]*backdrop|theme-gallery__editor-overlay|toast)\b/i;
const CONTENT_APP_REGION_SELECTOR =
  /(^|[,{\s])\.(?:msg|transcript|reasoning|tool|process-card|composer|compaction|turn-collapse)(?:__|-|[\s,{.#:]|$)/i;

export function shellFromEnv(env = process.env) {
  const shell = (env.REASONIX_SHELL ?? "").trim().toLowerCase();
  if (shell === "") return "browser";
  if (shell === "electron") return "electron";
  throw new Error(`REASONIX_SHELL must be "electron" or unset, got ${JSON.stringify(shell)}`);
}

function enclosingSelector(css, offset) {
  const open = css.lastIndexOf("{", offset);
  if (open < 0) return "";
  const close = css.lastIndexOf("}", open);
  return css.slice(close + 1, open);
}

function rewriteNoDrag(css) {
  return css.replace(DRAG_VALUE, (match, sep, value, offset) => {
    if (value === "drag") return `-webkit-app-region${sep}drag`;
    const selector = enclosingSelector(css, offset);
    if (CONTENT_APP_REGION_SELECTOR.test(selector)) return match;
    if (CHROME_NO_DRAG_SELECTOR.test(selector)) return `-webkit-app-region${sep}no-drag`;
    return match;
  });
}

function stripContentAppRegion(css) {
  return css.replace(/([^{}]+)\{([^{}]*)\}/g, (rule, selector, body) => {
    if (!CONTENT_APP_REGION_SELECTOR.test(selector) || !body.includes("-webkit-app-region")) return rule;
    return `${selector}{${body.replace(APP_REGION_DECL, "")}}`;
  });
}

export function rewriteDragRegions(css, shell) {
  if (shell !== "electron") return css;
  return stripContentAppRegion(rewriteNoDrag(css));
}
