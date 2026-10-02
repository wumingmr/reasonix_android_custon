import { spawnSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const historyTests = {
  "history-3-5": "TestTaggedHistory1383To1385",
  "history-6-7": "TestTaggedHistory1386To1387",
  "history-8-9": "TestTaggedHistory1388To1389",
  "history-10-11": "TestTaggedHistory13810To13811",
};
export const groups = ["A-B", "C", "D", "E-H", "I-M", "N-P", "Q-S", "T-Z", ...Object.keys(historyTests)];
export const conptyProbe = "TestWindowsTerminalProcessConPTYSmoke";
export const filters = {
  "A-B": { skip: "^Test[C-Z]" },
  "C": { run: "^TestC" },
  "D": { run: "^TestD" },
  "E-H": { run: "^Test[E-H]" },
  "I-M": { run: "^Test[I-M]" },
  "N-P": { run: "^Test[N-P]" },
  "Q-S": { run: "^Test[Q-S]" },
  "T-Z": { run: "^Test[T-Z]", skip: `^(${[conptyProbe, ...Object.values(historyTests)].join("|")})$` },
  ...Object.fromEntries(Object.entries(historyTests).map(([group, name]) => [group, { run: `^${name}$` }])),
};

export function owners(name) {
  const assigned = groups.filter(group => {
    const { run, skip } = filters[group];
    return (!run || new RegExp(run).test(name)) && (!skip || !new RegExp(skip).test(name));
  });
  if (name === conptyProbe) assigned.push("conpty-probe");
  return assigned;
}

export function inventoryFromJSON(output) {
  const names = new Map();
  for (const line of output.split(/\r?\n/).filter(Boolean)) {
    const event = JSON.parse(line);
    const name = event.Output?.trim();
    if (event.Action === "output" && /^(Test|Example|Fuzz)\S*$/u.test(name ?? "")) {
      names.set(`${event.Package}/${name}`, name);
    }
  }
  if (!names.size) throw new Error("Go returned no desktop test inventory");
  return names;
}

export function verifyInventory(inventory, requireProbe = process.platform === "win32") {
  const counts = Object.fromEntries([...groups, "conpty-probe"].map(group => [group, 0]));
  for (const [key, name] of inventory) {
    const assigned = owners(name);
    if (assigned.length !== 1) throw new Error(`${key} belongs to ${assigned.length} test groups`);
    counts[assigned[0]]++;
  }
  for (const group of groups) {
    if (!counts[group]) throw new Error(`Empty desktop Windows test group: ${group}`);
  }
  if (requireProbe && counts["conpty-probe"] !== 1) throw new Error("Missing or duplicated native ConPTY probe");
  return counts;
}

export function testArgs(group, race = false) {
  if (!groups.includes(group)) throw new Error(`Unknown desktop Windows test group: ${group}`);
  const { run, skip } = filters[group];
  // go's default alarm is 10m; the desktop race sweep already exceeded it and
  // the unpartitioned suite needs ~25m locally. Give every group the headroom.
  return ["test", ...(race ? ["-race"] : []), "-timeout=25m", ...(run ? ["-run", run] : []), ...(skip ? ["-skip", skip] : []), "./..."];
}

function main(group, mode) {
  if (mode && mode !== "--race") throw new Error(`Unknown test mode: ${mode}`);
  const race = mode === "--race";
  const selected = group === "--all" ? groups : group === "--verify" ? [] : [group];
  const commands = selected.map(name => testArgs(name, race));
  // Ask Go for the current platform's inventory, including build-tagged tests,
  // examples and fuzz seeds. JSON is used only for listing, not test execution.
  const listed = spawnSync("go", ["test", ...(race ? ["-race"] : []), "-list", ".", "-json", "./..."], { encoding: "utf8", maxBuffer: 16 << 20 });
  if (listed.error) throw listed.error;
  if (listed.status !== 0) {
    process.stderr.write(listed.stdout + listed.stderr);
    return listed.status ?? 1;
  }
  console.log("Desktop test partition:", verifyInventory(inventoryFromJSON(listed.stdout)));
  for (const args of commands) {
    console.log(`go ${args.join(" ")}`);
    const result = spawnSync("go", args, { stdio: "inherit" });
    if (result.error) throw result.error;
    if (result.status !== 0) return result.status ?? 1;
  }
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv[2], process.argv[3]);
}
