declare module "reasonix-playwright-injected" {
  export class InjectedScript {
    constructor(window: Window, options: { isUnderTest: boolean; sdkLanguage: string; testIdAttributeName: string; stableRafCount: number; browserName: string; customEngines: unknown[]; isUtilityWorld: boolean });
    ariaSnapshotWithRefs(root: Element, options: { mode: "ai" }): { text: string };
    parseSelector(selector: string): unknown;
    querySelectorAll(selector: unknown, root: Document | Element): Element[];
    elementState(element: Element, state: string): { matches: boolean; received: string };
    utils: { getAriaRole(element: Element): string | null; getElementAccessibleNameText(element: Element, includeHidden: boolean): string };
  }
}
