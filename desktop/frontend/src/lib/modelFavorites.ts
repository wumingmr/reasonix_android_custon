export const MODEL_FAVORITES_STORAGE_KEY = "reasonix-model-favorites-v1";

type ReadableStorage = Pick<Storage, "getItem">;
type WritableStorage = Pick<Storage, "setItem">;

function browserStorage(): Storage | undefined {
  return typeof localStorage === "undefined" ? undefined : localStorage;
}

export function normalizeModelFavorites(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return [...new Set(value.filter((item): item is string => typeof item === "string" && item.trim().length > 0))];
}

export function readModelFavorites(storage: ReadableStorage | undefined = browserStorage()): Set<string> {
  if (!storage) return new Set();
  try {
    const raw = storage.getItem(MODEL_FAVORITES_STORAGE_KEY);
    return new Set(raw ? normalizeModelFavorites(JSON.parse(raw)) : []);
  } catch {
    return new Set();
  }
}

export function writeModelFavorites(favorites: ReadonlySet<string>, storage: WritableStorage | undefined = browserStorage()): boolean {
  if (!storage) return false;
  try {
    storage.setItem(MODEL_FAVORITES_STORAGE_KEY, JSON.stringify([...favorites].sort()));
    return true;
  } catch {
    return false;
  }
}
