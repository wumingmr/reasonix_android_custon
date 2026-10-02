import {
  MODEL_FAVORITES_STORAGE_KEY,
  normalizeModelFavorites,
  readModelFavorites,
  writeModelFavorites,
} from "../lib/modelFavorites";

const memory = new Map<string, string>();
const storage = {
  getItem: (key: string) => memory.get(key) ?? null,
  setItem: (key: string, value: string) => { memory.set(key, value); },
};

if (readModelFavorites(storage).size !== 0) throw new Error("missing favorites should read as empty");

memory.set(MODEL_FAVORITES_STORAGE_KEY, "not-json");
if (readModelFavorites(storage).size !== 0) throw new Error("malformed favorites should read as empty");

memory.set(MODEL_FAVORITES_STORAGE_KEY, JSON.stringify(["b/model", 4, "a/model", "b/model", ""]));
const restored = [...readModelFavorites(storage)];
if (restored.join("|") !== "b/model|a/model") {
  throw new Error(`valid favorite refs were not restored safely: ${restored}`);
}

const normalized = normalizeModelFavorites(["a/model", null, "a/model", "b/model"]);
if (normalized.join("|") !== "a/model|b/model") throw new Error(`favorites were not normalized: ${normalized}`);

if (!writeModelFavorites(new Set(["z/model", "a/model"]), storage)) {
  throw new Error("favorites write unexpectedly failed");
}
if (memory.get(MODEL_FAVORITES_STORAGE_KEY) !== JSON.stringify(["a/model", "z/model"])) {
  throw new Error(`favorites write was not stable: ${memory.get(MODEL_FAVORITES_STORAGE_KEY)}`);
}

console.log("model favorites: PASS");
