// This is a campaign identity, not an application version. Minor upgrades must
// not reset it. No configuration, session or updater data is written here.
export const STUDIO_DISCOVERY_KEY = "reasonix.studio-introduction.v1";
export const STUDIO_DOWNLOAD_URL = "https://reasonix.io/?download=studio#start";

export function isStableOneVersion(version: string): boolean {
  return /^v?1\.\d+\.\d+$/.test(version.trim());
}

export function studioIntroductionPending(): boolean {
  try {
    return window.localStorage.getItem(STUDIO_DISCOVERY_KEY) === null;
  } catch {
    // Without durable dismissal, automatic promotion could repeat every launch.
    return false;
  }
}

export function claimStudioIntroduction(): boolean {
  if (!studioIntroductionPending()) return false;
  try {
    window.localStorage.setItem(STUDIO_DISCOVERY_KEY, "seen");
    return true;
  } catch {
    return false;
  }
}
