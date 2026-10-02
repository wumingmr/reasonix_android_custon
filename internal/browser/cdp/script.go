package cdp

// bootstrapJS installs the executor's isolated-world helper. It runs once per
// document in a world the page cannot reach, so refs, the take-over counter,
// and the agent-input window are out of reach of page script. Every later
// evaluate is one call into __rx.
const bootstrapJS = `
(() => {
const W = {refs: new Map(), next: 1, userSeq: 0, agentUntil: 0};
globalThis.__rx = W;
const mark = (e) => { if (e.isTrusted && Date.now() > W.agentUntil) W.userSeq++; };
for (const t of ['pointerdown', 'keydown', 'wheel']) {
  document.addEventListener(t, mark, {capture: true, passive: true});
}
const clip = (s, n) => s.length > n ? s.slice(0, n) + '…' : s;
const flat = (s) => (s || '').replace(/\s+/g, ' ').trim();
const named = (s) => s ? ' ' + JSON.stringify(s) : '';
const ACTIONABLE = 'a[href],button,input,select,textarea,summary,[role],[tabindex],[contenteditable]';

W.window = (ms) => { W.agentUntil = Date.now() + ms; return W.userSeq; };
W.state = () => ({userSeq: W.userSeq, url: location.href, title: document.title, ready: document.readyState});
W.element = (ref) => { const el = W.refs.get(ref); return (el && el.isConnected) ? el : null; };

W.rect = (ref) => {
  const el = W.element(ref);
  if (!el) return null;
  try { el.scrollIntoView({block: 'center', inline: 'center'}); } catch (e) {}
  const r = el.getBoundingClientRect();
  const tag = el.tagName.toLowerCase();
  const type = flat((el.getAttribute('type') || '')).toLowerCase();
  if (r.width <= 0 && r.height <= 0) return {hidden: true, tag: tag, type: type};
  return {x: r.left + r.width / 2, y: r.top + r.height / 2, width: r.width, height: r.height, tag: tag, type: type};
};

W.focus = (ref) => {
  const el = W.element(ref);
  if (!el) return {ok: false, reason: 'the ref is not on this page any more'};
  try { el.focus({preventScroll: false}); } catch (e) {}
  const active = document.activeElement;
  return {ok: active === el || el.contains(active), reason: 'the element refused focus'};
};

W.select = (ref, wanted) => {
  const el = W.element(ref);
  if (!el) return {ok: false, reason: 'the ref is not on this page any more'};
  if (el.tagName !== 'SELECT') return {ok: false, reason: 'element ' + el.tagName.toLowerCase() + ' is not a select'};
  const opts = Array.from(el.options);
  const chosen = [];
  for (const want of wanted) {
    const hit = opts.find((o) => o.value === want) || opts.find((o) => flat(o.label || o.textContent) === want);
    if (!hit) return {ok: false, reason: 'no option has the value or label ' + JSON.stringify(want)};
    chosen.push(hit);
  }
  if (chosen.length > 1 && !el.multiple) return {ok: false, reason: 'this select accepts one option'};
  for (const o of opts) o.selected = false;
  for (const o of chosen) o.selected = true;
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return {ok: true, selected: chosen.map((o) => o.value)};
};

const SKIP = new Set(['script', 'style', 'noscript', 'template', 'head', 'meta', 'link', 'title', 'svg', 'path', 'br']);
const ROLES = {
  a: 'link', button: 'button', select: 'combobox', textarea: 'textbox', summary: 'disclosure',
  h1: 'heading', h2: 'heading', h3: 'heading', h4: 'heading', h5: 'heading', h6: 'heading',
  img: 'image', table: 'table', th: 'columnheader', td: 'cell', li: 'listitem', form: 'form',
  nav: 'navigation', main: 'main', header: 'banner', footer: 'contentinfo', dialog: 'dialog',
  option: 'option', label: 'label', iframe: 'iframe',
};
const INPUT_ROLES = {
  checkbox: 'checkbox', radio: 'radio', submit: 'button', button: 'button', reset: 'button',
  file: 'file-input', range: 'slider', color: 'color-picker', hidden: '',
};

const typeOf = (el) => flat(el.getAttribute('type') || '').toLowerCase();

const roleOf = (el) => {
  const explicit = flat(el.getAttribute('role'));
  if (explicit) return explicit.split(' ')[0];
  const tag = el.tagName.toLowerCase();
  if (tag === 'input') {
    const t = typeOf(el);
    return Object.prototype.hasOwnProperty.call(INPUT_ROLES, t) ? INPUT_ROLES[t] : 'textbox';
  }
  return ROLES[tag] || '';
};

// nameOf follows the accessible-name order that matters here: an explicit
// label beats the placeholder a user only sees while the field is empty, and
// an element's own text names it only when it has nothing else to say, so a
// container is not named after everything inside it.
const nameOf = (el, allowText) => {
  const aria = flat(el.getAttribute('aria-label'));
  if (aria) return clip(aria, 160);
  if (el.labels && el.labels.length) {
    const label = flat(el.labels[0].textContent);
    if (label) return clip(label, 160);
  }
  const attr = flat(el.getAttribute('alt') || el.getAttribute('title') || el.getAttribute('placeholder'));
  if (attr) return clip(attr, 160);
  if (!allowText) return '';
  return clip(flat(el.innerText || el.textContent), 160);
};

const interactive = (el) => {
  const tag = el.tagName.toLowerCase();
  if (tag === 'a') return el.hasAttribute('href');
  if (tag === 'button' || tag === 'select' || tag === 'textarea' || tag === 'summary') return true;
  if (tag === 'input') return typeOf(el) !== 'hidden';
  if (el.isContentEditable) return true;
  if (el.hasAttribute('tabindex') || el.hasAttribute('onclick')) return true;
  const role = flat(el.getAttribute('role'));
  return ['button', 'link', 'checkbox', 'radio', 'tab', 'menuitem', 'switch', 'option', 'textbox', 'searchbox'].includes(role);
};

const visible = (el) => {
  if (typeof el.checkVisibility === 'function') return el.checkVisibility({checkVisibilityCSS: true});
  return el.getClientRects().length > 0;
};

const attrsOf = (el) => {
  const out = [];
  if (el.disabled) out.push('disabled');
  if (el.checked) out.push('checked');
  if (el.required) out.push('required');
  if (el.getAttribute('aria-expanded') === 'true') out.push('expanded');
  if (el.tagName === 'A' && el.getAttribute('href')) out.push('href=' + clip(el.getAttribute('href'), 120));
  if ('value' in el && typeOf(el) !== 'password' && flat(el.value)) out.push('value=' + JSON.stringify(clip(flat(el.value), 80)));
  if (typeOf(el) === 'password' && el.value) out.push('value=(hidden)');
  return out.length ? ' [' + out.join(' ') + ']' : '';
};

W.snapshot = (selector, budget) => {
  W.refs = new Map();
  W.next = 1;
  let root = document.body || document.documentElement;
  if (selector) {
    root = document.querySelector(selector);
    if (!root) return {error: 'no element matches the selector'};
  }
  const lines = [];
  let left = budget;
  const pad = (d) => '  '.repeat(Math.min(d, 20));
  const walk = (el, depth) => {
    if (left <= 0 || depth > 40) return;
    for (const node of el.childNodes) {
      if (left <= 0) return;
      if (node.nodeType === 3) {
        const text = flat(node.textContent);
        if (text) { left--; lines.push(pad(depth) + '- text: ' + clip(text, 200)); }
        continue;
      }
      if (node.nodeType !== 1 || SKIP.has(node.tagName.toLowerCase()) || !visible(node)) continue;
      const role = roleOf(node);
      const leaf = node.children.length === 0;
      if (interactive(node)) {
        const ref = 'e' + (W.next++);
        W.refs.set(ref, node);
        left--;
        lines.push(pad(depth) + '- ' + (role || 'control') + named(nameOf(node, true)) + attrsOf(node) + ' [ref=' + ref + ']');
        // A control's own text is already its name; walk into it only when it
        // wraps another control the model could act on.
        if (!leaf && node.tagName !== 'SELECT' && node.tagName !== 'TEXTAREA' && node.querySelector(ACTIONABLE)) walk(node, depth + 1);
        continue;
      }
      if (role) {
        left--;
        lines.push(pad(depth) + '- ' + role + named(nameOf(node, leaf)));
        if (!leaf) walk(node, depth + 1);
        continue;
      }
      walk(node, depth);
    }
  };
  walk(root, 0);
  return {
    url: location.href, title: document.title, tree: lines.join('\n'),
    refs: W.next - 1, userSeq: W.userSeq, truncated: left <= 0,
  };
};
})()
`
