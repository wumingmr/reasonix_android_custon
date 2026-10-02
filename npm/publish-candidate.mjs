import { execFileSync } from "node:child_process";
import { readdirSync } from "node:fs";
import path from "node:path";
import { publishPackages, stableDistTagFromEnv } from "./publish.mjs";

const [directory, version, candidateSha] = process.argv.slice(2);
if (!directory || !version || !candidateSha) {
  throw new Error("usage: publish-candidate.mjs DIRECTORY VERSION CANDIDATE_SHA");
}

const packages = readdirSync(directory)
  .filter(name => name.endsWith(".tgz"))
  .sort()
  .map(name => {
    const tarball = path.resolve(directory, name);
    const manifest = JSON.parse(execFileSync("tar", ["-xOf", tarball, "package/package.json"], { encoding: "utf8" }));
    return { name: manifest.name, manifest, tarball, dir: process.cwd() };
  });

publishPackages({ packages, version, candidateSha, stableDistTag: stableDistTagFromEnv() });
