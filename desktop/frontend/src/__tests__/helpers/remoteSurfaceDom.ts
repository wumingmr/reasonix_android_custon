import { JSDOM } from "jsdom";

// installRemoteSurfaceDom publishes a jsdom window as the process globals the
// remote surface suite renders against. The transcript measures through the
// element prototype and calls the global rAF, which jsdom exposes only on a
// visual window, so both are stubbed here rather than per test.
export function installRemoteSurfaceDom(): JSDOM {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.getComputedStyle = dom.window.getComputedStyle.bind(dom.window) as typeof getComputedStyle;
  const elementProto = dom.window.HTMLElement.prototype;
  Object.defineProperty(elementProto, "attachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(elementProto, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) { return this.classList.contains("transcript") ? 800 : 40; },
  });
  Object.defineProperty(elementProto, "offsetWidth", { configurable: true, get: () => 800 });
  Object.defineProperty(elementProto, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) { return this.classList.contains("transcript") ? 800 : 40; },
  });
  Object.defineProperty(elementProto, "clientWidth", { configurable: true, get: () => 800 });
  (elementProto as unknown as { scrollTo: (arg?: number | ScrollToOptions) => void }).scrollTo = function (
    this: HTMLElement,
    arg?: number | ScrollToOptions,
  ) {
    this.scrollTop = typeof arg === "number" ? arg : arg?.top ?? this.scrollTop;
  };
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame?.bind(dom.window) ?? ((cb: FrameRequestCallback) => setTimeout(() => cb(Date.now()), 16) as unknown as number);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame?.bind(dom.window) ?? ((handle: number) => clearTimeout(handle));
  Object.defineProperty(elementProto, "detachEvent", { configurable: true, value: () => {} });
  return dom;
}
