import { createHash } from "node:crypto";
import {
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  statSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const SHA_RE = /^[0-9a-f]{40}$/;
const VERSION_RE = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
const CANDIDATE_RE = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[0-9a-f]{12}-[0-9a-f]{12}$/;
export const desktopPlatforms = [
  "darwin-arm64", "darwin-amd64", "darwin-universal",
  "windows-amd64", "windows-arm64", "linux-amd64",
];
export const npmPackages = [
  "reasonix", "reasonix-cli-darwin-arm64", "reasonix-cli-darwin-x64",
  "reasonix-cli-linux-arm64", "reasonix-cli-linux-x64",
  "reasonix-cli-win32-arm64", "reasonix-cli-win32-x64",
];

export function artifactNamespace(purpose = "release") {
  if (!["release", "rehearsal"].includes(purpose)) throw new Error("invalid candidate purpose");
  return purpose === "rehearsal" ? "release-candidate-rehearsal" : "release-candidate";
}

function sha256(data) {
  return createHash("sha256").update(data).digest("hex");
}

function fileEntry(root, file) {
  const absolute = path.join(root, file);
  const stat = lstatSync(absolute);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size === 0) {
    throw new Error(`candidate contains an invalid file: ${file}`);
  }
  return { path: file.split(path.sep).join("/"), size: stat.size, sha256: sha256(readFileSync(absolute)) };
}

function walk(root, directory = root) {
  const files = [];
  for (const name of readdirSync(directory).sort()) {
    const absolute = path.join(directory, name);
    const stat = lstatSync(absolute);
    if (stat.isSymbolicLink()) throw new Error(`candidate contains a symlink: ${path.relative(root, absolute)}`);
    if (stat.isDirectory()) files.push(...walk(root, absolute));
    else files.push(fileEntry(root, path.relative(root, absolute)));
  }
  return files;
}

function requireIdentity(metadata) {
  const namespace = artifactNamespace(metadata.purpose);
  if (!VERSION_RE.test(metadata.version ?? "")) throw new Error("invalid candidate version");
  for (const key of ["sourceSHA", "buildControlSHA", "acceptanceControlSHA", "notesSourceSHA"]) {
    if (!SHA_RE.test(metadata[key] ?? "")) throw new Error(`invalid ${key}`);
  }
  if (!/^[0-9a-f]{64}$/.test(metadata.catalogSha256 ?? "")) throw new Error("invalid catalogSha256");
  if (!/^[0-9a-f]{64}$/.test(metadata.renderedNotesSha256 ?? "")) throw new Error("invalid renderedNotesSha256");
  if (!/^[1-9][0-9]*$/.test(String(metadata.runId ?? ""))) throw new Error("invalid runId");
  if (!/^[1-9][0-9]*$/.test(String(metadata.runAttempt ?? ""))) throw new Error("invalid runAttempt");
  if (!/^[1-9][0-9]*$/.test(String(metadata.payloadArtifactId ?? ""))) throw new Error("invalid payloadArtifactId");
  if (!/^[1-9][0-9]*$/.test(String(metadata.evidenceArtifactId ?? ""))) throw new Error("invalid evidenceArtifactId");
  if (metadata.payloadArtifactName !== `${namespace}-payload-${metadata.candidateId ?? candidateId(metadata.version, metadata.sourceSHA, metadata.catalogSha256)}`) {
    throw new Error("invalid payloadArtifactName");
  }
  if (metadata.evidenceArtifactName !== `${namespace}-evidence-${metadata.candidateId ?? candidateId(metadata.version, metadata.sourceSHA, metadata.catalogSha256)}`) {
    throw new Error("invalid evidenceArtifactName");
  }
  if (metadata.repository !== "esengine/DeepSeek-Reasonix") throw new Error("untrusted candidate repository");
  if (metadata.workflow !== ".github/workflows/release-candidate.yml") throw new Error("untrusted candidate workflow");
}

export function candidateId(version, sourceSHA, catalogSha256) {
  if (!VERSION_RE.test(version) || !SHA_RE.test(sourceSHA) || !/^[0-9a-f]{64}$/.test(catalogSha256)) {
    throw new Error("cannot derive candidate id from invalid identity");
  }
  return `v${version}-${sourceSHA.slice(0, 12)}-${catalogSha256.slice(0, 12)}`;
}

function requirePayloadLayout(root, files) {
  const names = new Set(files.map(file => file.path));
  for (const platform of desktopPlatforms) {
    const prefix = `desktop/${platform}/`;
    if (![...names].some(name => name.startsWith(prefix) && name.endsWith("/identity.json"))) {
      throw new Error(`candidate is missing Desktop bundle: ${platform}`);
    }
  }
  const cli = [
    "reasonix-darwin-amd64.tar.gz", "reasonix-darwin-arm64.tar.gz",
    "reasonix-linux-amd64.tar.gz", "reasonix-linux-arm64.tar.gz",
    "reasonix-windows-amd64.zip", "reasonix-windows-arm64.zip", "SHA256SUMS",
    "reasonix.rb",
  ];
  for (const name of cli) if (!names.has(`cli/${name}`)) throw new Error(`candidate is missing CLI asset: ${name}`);
  const tarballs = [...names].filter(name => name.startsWith("npm/") && name.endsWith(".tgz"));
  if (tarballs.length !== npmPackages.length) throw new Error(`candidate has ${tarballs.length} npm packages; expected ${npmPackages.length}`);
  for (const name of npmPackages) {
    if (!tarballs.some(file => path.basename(file).startsWith(`${name}-`))) throw new Error(`candidate is missing npm package: ${name}`);
  }
}

function desktopIdentity(payloadRoot, platform) {
  const identityPath = path.join(payloadRoot, "desktop", platform, "identity.json");
  const identity = JSON.parse(readFileSync(identityPath, "utf8"));
  if (identity.schema !== 1 || identity.platform !== platform) {
    throw new Error(`candidate has an invalid Desktop identity: ${platform}`);
  }
  return identity;
}

function requireDesktopIdentities(payloadRoot, metadata) {
  for (const platform of desktopPlatforms) {
    const identity = desktopIdentity(payloadRoot, platform);
    const expected = {
      sourceSHA: metadata.sourceSHA,
      controlSHA: metadata.buildControlSHA,
      tag: `desktop-v${metadata.version}`,
      version: `v${metadata.version}`,
      channel: "stable",
      signingFingerprint: metadata.desktopFingerprint,
      prefix: metadata.desktopPrefix,
    };
    for (const [key, value] of Object.entries(expected)) {
      if (identity[key] !== value) {
        throw new Error(`candidate Desktop identity mismatch for ${platform}: ${key}`);
      }
    }
  }
}

function requireAcceptanceReceipt(payloadRoot, files, metadata, item) {
  const evidence = files.find(file => file.path === item.evidencePath);
  if (item.status !== "passed" || !evidence) {
    throw new Error(`candidate acceptance evidence is missing: ${item.kind}`);
  }
  const receipt = JSON.parse(readFileSync(path.join(payloadRoot, item.evidencePath), "utf8"));
  if (receipt.schema !== 1 || receipt.kind !== item.kind || receipt.status !== "passed"
      || receipt.version !== `v${metadata.version}` || !/^[0-9a-f]{64}$/.test(receipt.sha256 ?? "")) {
    throw new Error(`candidate acceptance receipt is invalid: ${item.kind}`);
  }
  const platform = item.kind === "macos-universal-intel" ? "darwin-universal" : item.kind;
  const identity = desktopIdentity(payloadRoot, platform);
  const artifact = identity.files.find(file => file.sha256 === receipt.sha256);
  if (!artifact) throw new Error(`candidate acceptance receipt does not name a sealed file: ${item.kind}`);
  return { ...item, evidenceSha256: evidence.sha256, artifactSha256: receipt.sha256 };
}

// The notes are rendered once, at seal, because rendering reads GitHub: a later
// render can differ (a renamed author, a newly linked fix) or fail outright.
export const sealedNotesPath = "evidence/release-notes.md";

function requireSealedNotes(files, renderedSha256) {
  const notes = files.find(file => file.path === sealedNotesPath);
  if (!notes) throw new Error(`candidate is missing sealed release notes: ${sealedNotesPath}`);
  if (notes.sha256 !== renderedSha256) throw new Error("sealed release notes do not match renderedSha256");
}

export function sealCandidate(payloadRoot, metadata) {
  requireIdentity(metadata);
  const files = walk(payloadRoot);
  requirePayloadLayout(payloadRoot, files);
  requireDesktopIdentities(payloadRoot, metadata);
  const id = candidateId(metadata.version, metadata.sourceSHA, metadata.catalogSha256);
  const created = new Date(metadata.createdAt);
  const expires = new Date(metadata.expiresAt);
  if (!Number.isFinite(created.valueOf()) || !Number.isFinite(expires.valueOf()) || expires <= created) {
    throw new Error("invalid candidate validity window");
  }
  const acceptance = metadata.acceptance.map(item => requireAcceptanceReceipt(payloadRoot, files, metadata, item));
  requireSealedNotes(files, metadata.renderedNotesSha256);
  return {
    schema: 1,
    purpose: metadata.purpose ?? "release",
    candidateId: id,
    policyVersion: 1,
    version: metadata.version,
    sourceSHA: metadata.sourceSHA,
    control: { buildSHA: metadata.buildControlSHA, acceptanceSHA: metadata.acceptanceControlSHA },
    notes: {
      sourceSHA: metadata.notesSourceSHA,
      catalogSha256: metadata.catalogSha256,
      renderedSha256: metadata.renderedNotesSha256,
      renderedPath: sealedNotesPath,
    },
    source: {
      repository: metadata.repository,
      workflow: metadata.workflow,
      runId: String(metadata.runId),
      runAttempt: String(metadata.runAttempt),
      desktopPrefix: metadata.desktopPrefix,
      payloadArtifactId: String(metadata.payloadArtifactId),
      payloadArtifactName: metadata.payloadArtifactName,
      evidenceArtifactId: String(metadata.evidenceArtifactId),
      evidenceArtifactName: metadata.evidenceArtifactName,
    },
    signing: { desktopFingerprint: metadata.desktopFingerprint },
    acceptance,
    validity: { createdAt: created.toISOString(), expiresAt: expires.toISOString(), revoked: false },
    files,
  };
}

export function verifyCandidate(payloadRoot, record, now = new Date(), purpose = "release") {
  artifactNamespace(purpose);
  if ((record.purpose ?? "release") !== purpose) throw new Error("candidate purpose mismatch; rehearsal cannot be published");
  if (record.schema !== 1 || record.policyVersion !== 1) throw new Error("unsupported candidate schema");
  requireIdentity({
    purpose: record.purpose,
    version: record.version,
    sourceSHA: record.sourceSHA,
    buildControlSHA: record.control?.buildSHA,
    acceptanceControlSHA: record.control?.acceptanceSHA,
    notesSourceSHA: record.notes?.sourceSHA,
    catalogSha256: record.notes?.catalogSha256,
    renderedNotesSha256: record.notes?.renderedSha256,
    repository: record.source?.repository,
    workflow: record.source?.workflow,
    runId: record.source?.runId,
    runAttempt: record.source?.runAttempt,
    payloadArtifactId: record.source?.payloadArtifactId,
    payloadArtifactName: record.source?.payloadArtifactName,
    evidenceArtifactId: record.source?.evidenceArtifactId,
    evidenceArtifactName: record.source?.evidenceArtifactName,
    candidateId: record.candidateId,
  });
  if (!CANDIDATE_RE.test(record.candidateId) || record.candidateId !== candidateId(record.version, record.sourceSHA, record.notes.catalogSha256)) {
    throw new Error("candidate id does not match its immutable inputs");
  }
  if (record.validity?.revoked !== false) throw new Error("candidate is revoked");
  if (new Date(record.validity?.expiresAt).valueOf() <= now.valueOf()) throw new Error("candidate has expired");
  const actual = walk(payloadRoot);
  requirePayloadLayout(payloadRoot, actual);
  if (JSON.stringify(actual) !== JSON.stringify(record.files)) throw new Error("candidate payload digest mismatch");
  if (record.notes.renderedPath !== undefined) {
    if (record.notes.renderedPath !== sealedNotesPath) throw new Error("invalid sealed release notes path");
    requireSealedNotes(actual, record.notes.renderedSha256);
  }
  requireDesktopIdentities(payloadRoot, {
    version: record.version,
    sourceSHA: record.sourceSHA,
    buildControlSHA: record.control.buildSHA,
    desktopFingerprint: record.signing.desktopFingerprint,
    desktopPrefix: record.source.desktopPrefix,
  });
  const passed = new Set((record.acceptance ?? []).filter(item => item.status === "passed").map(item => item.kind));
  for (const required of ["windows-amd64", "windows-arm64", "macos-universal-intel"]) {
    if (!passed.has(required)) throw new Error(`candidate is missing acceptance evidence: ${required}`);
  }
  for (const item of record.acceptance) {
    const evidence = actual.find(file => file.path === item.evidencePath);
    if (!evidence || evidence.sha256 !== item.evidenceSha256) throw new Error(`candidate acceptance receipt mismatch: ${item.kind}`);
    const validated = requireAcceptanceReceipt(payloadRoot, actual, {
      version: record.version,
    }, item);
    if (validated.artifactSha256 !== item.artifactSha256) {
      throw new Error(`candidate acceptance artifact mismatch: ${item.kind}`);
    }
  }
  return record;
}

function parseMetadata(env) {
  const acceptance = JSON.parse(env.RELEASE_ACCEPTANCE_JSON ?? "[]");
  return {
    purpose: env.RELEASE_CANDIDATE_PURPOSE ?? "release",
    version: env.RELEASE_VERSION,
    sourceSHA: env.RELEASE_SOURCE_SHA,
    buildControlSHA: env.RELEASE_BUILD_CONTROL_SHA,
    acceptanceControlSHA: env.RELEASE_ACCEPTANCE_CONTROL_SHA,
    notesSourceSHA: env.RELEASE_NOTES_SOURCE_SHA,
    catalogSha256: env.RELEASE_CATALOG_SHA256,
    renderedNotesSha256: env.RELEASE_RENDERED_NOTES_SHA256,
    repository: env.GITHUB_REPOSITORY,
    workflow: env.RELEASE_WORKFLOW,
    runId: env.GITHUB_RUN_ID,
    runAttempt: env.GITHUB_RUN_ATTEMPT,
    payloadArtifactId: env.RELEASE_PAYLOAD_ARTIFACT_ID,
    payloadArtifactName: env.RELEASE_PAYLOAD_ARTIFACT_NAME,
    evidenceArtifactId: env.RELEASE_EVIDENCE_ARTIFACT_ID,
    evidenceArtifactName: env.RELEASE_EVIDENCE_ARTIFACT_NAME,
    desktopPrefix: env.RELEASE_DESKTOP_PREFIX,
    desktopFingerprint: env.RELEASE_DESKTOP_FINGERPRINT,
    createdAt: env.RELEASE_CREATED_AT,
    expiresAt: env.RELEASE_EXPIRES_AT,
    acceptance,
  };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const [command, payload, recordPath, catalogSha256] = process.argv.slice(2);
  if (command === "seal") {
    mkdirSync(path.dirname(recordPath), { recursive: true });
    writeFileSync(recordPath, `${JSON.stringify(sealCandidate(payload, parseMetadata(process.env)), null, 2)}\n`);
  } else if (command === "verify" || command === "verify-rehearsal") {
    verifyCandidate(payload, JSON.parse(readFileSync(recordPath, "utf8")), new Date(), command === "verify-rehearsal" ? "rehearsal" : "release");
    process.stdout.write(`${JSON.stringify(JSON.parse(readFileSync(recordPath, "utf8")))}\n`);
  } else if (command === "id") {
    process.stdout.write(`${candidateId(payload, recordPath, catalogSha256)}\n`);
  } else {
    throw new Error("usage: release-candidate.mjs seal|verify|verify-rehearsal PAYLOAD RECORD | id VERSION SOURCE_SHA CATALOG_SHA256");
  }
}
