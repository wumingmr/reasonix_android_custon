import type { QualityFloor, TabMeta } from "./types";

export interface QualityFloorBindings {
  SetQualityFloor(floor: string): Promise<void>;
  SetQualityFloorForTab(tabID: string, floor: string): Promise<void>;
  // Model-free exit from the delivery pause; see desktop/delivery_accept.go.
  AcceptDelivery(): Promise<void>;
  AcceptDeliveryToTab(tabID: string): Promise<void>;
}

export function normalizeQualityFloor(floor: string): QualityFloor {
	void floor;
	return "standard";
}

// The mock preserves the retired bridge surface while matching the host's
// standard-only compatibility behavior.
export function makeMockQualityFloorBindings(
  tabs: () => TabMeta[],
  setTabs: (next: TabMeta[]) => void,
): QualityFloorBindings {
	const applyToTab = (tabID: string, floor: string) => {
		const next = normalizeQualityFloor(floor);
		setTabs(tabs().map((tab) => (tab.id === tabID ? { ...tab, qualityFloor: next, floorInferred: false } : tab)));
  };
  return {
    async SetQualityFloor(floor: string) {
      const active = tabs().find((tab) => tab.active);
      if (active) applyToTab(active.id, floor);
    },
    async SetQualityFloorForTab(tabID: string, floor: string) {
      applyToTab(tabID, floor);
    },
    async AcceptDelivery() {},
    async AcceptDeliveryToTab(_tabID: string) {},
  };
}
