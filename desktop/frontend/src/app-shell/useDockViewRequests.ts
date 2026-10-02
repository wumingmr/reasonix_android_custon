import { useLayoutEffect, useState, useSyncExternalStore } from "react";
import { DockNavigation, type DockRequests } from "./dockNavigation";

export function useDockViewRequests(scope: string, view: string | null, incoming: DockRequests, owner?: DockNavigation, openViews?: readonly string[]): DockRequests {
  const [local] = useState(() => new DockNavigation());
  const navigation = owner ?? local;
  const snapshot = useSyncExternalStore(navigation.subscribe, navigation.getSnapshot, navigation.getSnapshot);
  useLayoutEffect(() => { navigation.commit(scope, view, incoming, openViews); });
  useLayoutEffect(() => owner ? undefined : local.attach(), [local, owner]);
  return view && navigation.matches(scope, view) ? snapshot : navigation.restoredView(scope, view);
}
