import { createHash } from "node:crypto";
import { createReadStream, readFileSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

export async function verifyArchive(artifact, archivePath) {
  if (!Number.isSafeInteger(artifact.id) || artifact.id <= 0 || artifact.expired !== false ||
      !/^sha256:[0-9a-f]{64}$/.test(artifact.digest ?? "")) {
    throw new Error("artifact has no trustworthy active archive digest");
  }
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(archivePath)) hash.update(chunk);
  if (`sha256:${hash.digest("hex")}` !== artifact.digest) {
    throw new Error(`artifact ${artifact.id} archive digest mismatch`);
  }
  return artifact.digest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const [metadataPath, archivePath] = process.argv.slice(2);
  if (!metadataPath || !archivePath) throw new Error("usage: verify-release-artifact-archive.mjs METADATA_JSON ARCHIVE_ZIP");
  console.log(await verifyArchive(JSON.parse(readFileSync(metadataPath, "utf8")), archivePath));
}
