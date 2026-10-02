import { classifyPaths } from "../../../scripts/ci-paths.mjs";

export function memoryAffected(files) {
  return classifyPaths(files).flags.memory;
}

export function memoryMode(files) {
  const { memory, memory_full: full } = classifyPaths(files).flags;
  return !memory ? "off" : full ? "full" : "short";
}
