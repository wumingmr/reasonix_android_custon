import {
  modeFromAxes,
  modeHasAutoApproveTools,
  modeHasPlan,
  normalizeCollaborationMode,
  normalizeMode,
  normalizeToolApprovalMode,
  type CollaborationMode,
  type GoalStatus,
  type GoalLifecycleView,
  type Meta,
  type Mode,
  type TabMeta,
  type QualityFloor,
  type ToolApprovalMode,
} from "./types";

export type ComposerProfileField = "collaborationMode" | "toolApprovalMode" | "goal" | "qualityFloor";

export type ComposerProfilePending = Partial<Record<ComposerProfileField, true>>;

export interface ComposerProfile {
  collaborationMode: CollaborationMode;
  goalDraftMode: boolean;
  toolApprovalMode: ToolApprovalMode;
  goal: string;
  qualityFloor: QualityFloor;
  pending: ComposerProfilePending;
  /** Session the values were read for; "" while the surface has none. */
  owner?: string;
}

export type ComposerProfilesByTab = Record<string, ComposerProfile>;
export type UserPlanModeIntents = Record<string, true>;

const profileFields: ComposerProfileField[] = ["collaborationMode", "toolApprovalMode", "goal", "qualityFloor"];

export const defaultComposerProfile: ComposerProfile = Object.freeze({
  collaborationMode: "normal",
  goalDraftMode: false,
  toolApprovalMode: "workspace-write",
  goal: "",
  qualityFloor: "standard",
  pending: {},
});

function activeGoal(goal?: string, status?: GoalStatus, view?: GoalLifecycleView): string {
  const trimmed = (view?.objective ?? goal ?? "").trim();
  if (!trimmed) return "";
  if (view) return view.phase === "complete" ? "" : trimmed;
  if (status && status !== "running") return "";
  return trimmed;
}

type ComposerProfileOwnerSource = { sessionId?: string; session?: { sessionId?: string } | null; sessionPath?: string } | null | undefined;

export function composerProfileOwner(source: ComposerProfileOwnerSource): string {
  return (source?.session?.sessionId || source?.sessionId || source?.sessionPath || "").trim();
}

// A surface gaining its first session keeps its profile; moving between two
// sessions does not, because a preset belongs to the session it was set for.
export function composerProfileOwnersConflict(a: string | undefined, b: string | undefined): boolean {
  return Boolean(a && b && a !== b);
}

export function composerProfileForOwner(profile: ComposerProfile | undefined, owner: string): ComposerProfile | undefined {
  return profile && !composerProfileOwnersConflict(profile.owner, owner) ? profile : undefined;
}

function profileWithPending(profile: Omit<ComposerProfile, "pending">, pending: ComposerProfilePending = {}): ComposerProfile {
  return { ...profile, pending };
}

function fallbackToolApprovalMode(rawMode: string | undefined, fallback?: ToolApprovalMode | null): ToolApprovalMode | undefined {
  if ((rawMode ?? "").trim() !== "") return undefined;
  return fallback ? normalizeToolApprovalMode(fallback) : undefined;
}

export function composerProfileFromTab(tab?: TabMeta | null, fallback?: ToolApprovalMode | null): ComposerProfile {
  if (!tab) return { ...defaultComposerProfile, pending: {} };
  const legacyMode = normalizeMode(tab.mode);
  const goal = activeGoal(tab.goal, tab.goalStatus, tab.goalView);
  return profileWithPending({
    collaborationMode: normalizeCollaborationMode(tab.collaborationMode, goal, legacyMode),
    goalDraftMode: false,
    toolApprovalMode: normalizeToolApprovalMode(
      tab.toolApprovalMode,
      legacyMode,
      false,
      fallbackToolApprovalMode(tab.toolApprovalMode, fallback),
    ),
    goal,
    qualityFloor: tab.qualityFloor ?? "standard",
    owner: composerProfileOwner(tab),
  });
}

export function composerProfileFromMeta(meta?: Meta | null, legacyMode?: Mode, fallback?: ToolApprovalMode | null): ComposerProfile {
  if (!meta) return { ...defaultComposerProfile, pending: {} };
  const fallbackMode = normalizeMode(legacyMode);
  const goal = activeGoal(meta.goal, meta.goalStatus, meta.goalView);
  const toolApprovalMode = normalizeToolApprovalMode(
    meta.toolApprovalMode,
    fallbackMode,
    meta.autoApproveTools ?? meta.bypass,
    fallbackToolApprovalMode(meta.toolApprovalMode, fallback),
  );
  return profileWithPending({
    collaborationMode: normalizeCollaborationMode(meta.collaborationMode, goal, fallbackMode),
    goalDraftMode: false,
    toolApprovalMode,
    goal,
    qualityFloor: meta.qualityFloor ?? "standard",
    owner: composerProfileOwner(meta),
  });
}

function fieldValue(profile: ComposerProfile, field: ComposerProfileField): string {
  return profile[field];
}

function assignField(profile: ComposerProfile, field: ComposerProfileField, value: string) {
  switch (field) {
    case "collaborationMode":
      profile.collaborationMode = value as CollaborationMode;
      return;
    case "toolApprovalMode":
      profile.toolApprovalMode = value as ToolApprovalMode;
      return;
    case "goal":
      profile.goal = value;
      return;
    case "qualityFloor":
      profile.qualityFloor = value as QualityFloor;
      return;
  }
}

function profilesEqual(a: ComposerProfile | undefined, b: ComposerProfile | undefined): boolean {
  if (!a || !b) return a === b;
  return a.collaborationMode === b.collaborationMode
    && a.goalDraftMode === b.goalDraftMode
    && a.toolApprovalMode === b.toolApprovalMode
    && a.goal === b.goal
    && a.qualityFloor === b.qualityFloor
    && (a.owner ?? "") === (b.owner ?? "")
    && profileFields.every((field) => Boolean(a.pending[field]) === Boolean(b.pending[field]));
}

export function reconcileComposerProfile(current: ComposerProfile | undefined, backend: ComposerProfile): ComposerProfile {
  if (!current || composerProfileOwnersConflict(current.owner, backend.owner)) return { ...backend, pending: {} };

  const pending: ComposerProfilePending = {};
  const next: ComposerProfile = { ...backend, pending };

  for (const field of profileFields) {
    if (!current.pending[field]) continue;
    if (fieldValue(current, field) === fieldValue(backend, field)) continue;
    pending[field] = true;
    assignField(next, field, fieldValue(current, field));
  }

  if (current.goalDraftMode && !backend.goal && !next.goal) {
    next.goalDraftMode = true;
  }
  if (next.goal) {
    next.goalDraftMode = false;
  }

  return next;
}

export function hydrateComposerProfilesFromTabs(current: ComposerProfilesByTab, tabs: TabMeta[]): ComposerProfilesByTab {
  const next: ComposerProfilesByTab = {};
  let changed = false;

  for (const tab of tabs) {
    const previous = composerProfileForOwner(current[tab.id], composerProfileOwner(tab));
    const profile = reconcileComposerProfile(previous, composerProfileFromTab(tab, previous?.toolApprovalMode));
    next[tab.id] = profile;
    if (!profilesEqual(current[tab.id], profile)) changed = true;
  }

  for (const id of Object.keys(current)) {
    if (!next[id]) changed = true;
  }

  return changed ? next : current;
}

export function hydrateComposerProfileFromMeta(current: ComposerProfilesByTab, tabId: string, meta: Meta, tab?: TabMeta | null): ComposerProfilesByTab {
  const owner = composerProfileOwner(meta);
  if (composerProfileOwnersConflict(composerProfileOwner(tab), owner)) return current;
  const previous = composerProfileForOwner(current[tabId], owner);
  const backend = composerProfileFromMeta(
    meta,
    previous ? composerProfileMode(previous) : undefined,
    previous?.toolApprovalMode,
  );
  const profile = reconcileComposerProfile(previous, backend);
  if (profilesEqual(previous, profile)) return current;
  return { ...current, [tabId]: profile };
}

export function patchComposerProfile(
  current: ComposerProfilesByTab,
  tabId: string,
  base: ComposerProfile | undefined,
  patch: Partial<Omit<ComposerProfile, "pending">>,
  pendingFields: ComposerProfileField[],
): ComposerProfilesByTab {
  const previous = composerProfileForOwner(current[tabId], base?.owner ?? "") ?? base ?? defaultComposerProfile;
  const pending: ComposerProfilePending = { ...previous.pending };
  for (const field of pendingFields) pending[field] = true;
  const profile: ComposerProfile = {
    ...previous,
    ...patch,
    pending,
  };
  if (profile.goal) {
    profile.goalDraftMode = false;
  }
  if (profilesEqual(previous, profile)) return current;
  return { ...current, [tabId]: profile };
}

export function composerProfileMode(profile: ComposerProfile): Mode {
  return modeFromAxes(profile.collaborationMode === "plan", normalizeToolApprovalMode(profile.toolApprovalMode) === "danger-full-access");
}

export function displayedComposerProfileCollaborationMode(profile: ComposerProfile): CollaborationMode {
  if (profile.goalDraftMode) return "goal";
  return profile.collaborationMode;
}

export function controllerComposerProfileCollaborationMode(profile: ComposerProfile): CollaborationMode {
  const displayed = displayedComposerProfileCollaborationMode(profile);
  return displayed === "goal" && !profile.goal ? "normal" : displayed;
}

export function composerProfileWithMode(mode: Mode): Partial<Omit<ComposerProfile, "pending">> {
  return {
    collaborationMode: modeHasPlan(mode) ? "plan" : "normal",
    goalDraftMode: false,
    toolApprovalMode: modeHasAutoApproveTools(mode) ? "workspace-write" : "read-only",
    goal: "",
  };
}

export function updateUserPlanModeIntent(
  current: UserPlanModeIntents,
  tabId: string | null | undefined,
  enabled: boolean,
): UserPlanModeIntents {
  if (!tabId) return current;
  if (enabled) {
    return current[tabId] ? current : { ...current, [tabId]: true };
  }
  if (!current[tabId]) return current;
  const next = { ...current };
  delete next[tabId];
  return next;
}

export function pruneUserPlanModeIntents(current: UserPlanModeIntents, tabIds: Iterable<string>): UserPlanModeIntents {
  const live = new Set(tabIds);
  let changed = false;
  const next: UserPlanModeIntents = {};
  for (const tabId of Object.keys(current)) {
    if (live.has(tabId)) {
      next[tabId] = true;
    } else {
      changed = true;
    }
  }
  return changed ? next : current;
}

export function shouldRestoreUserPlanMode(current: UserPlanModeIntents, tabId: string | null | undefined): boolean {
  return Boolean(tabId && current[tabId]);
}

export function resolvePlanRestoreTabId(eventTabId: string | null | undefined, activeTabId: string | null | undefined): string | null {
  return eventTabId || activeTabId || null;
}

export function shouldRestoreUserPlanModeForProfile(
  current: UserPlanModeIntents,
  tabId: string | null | undefined,
  profile?: Pick<ComposerProfile, "goal"> | null,
): boolean {
  return shouldRestoreUserPlanMode(current, tabId) && !profile?.goal.trim();
}
