import { desktopHost } from "./desktopHost";
export function hostEvents(name: string, cb: (...args: unknown[]) => void): (() => void) | null {
  const host = desktopHost();
  return host.kind === "none" ? null : host.events.on(name, cb);
}
