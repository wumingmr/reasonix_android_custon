// Read-only diagnostics. The token stays in the runner and is sent only to the
// Cloudflare API; reports omit visitor identities, response bodies, and rules.
import { createHash } from "node:crypto";

const token = process.env.CLOUDFLARE_API_TOKEN;
if (!token) throw new Error("CLOUDFLARE_API_TOKEN is not configured");
const api = "https://api.cloudflare.com/client/v4";
const report = (kind, data) => console.log(JSON.stringify({ kind, ...data }));

function errorClass(error) {
  const message = String(error.message || "");
  if (/cannot query field|unknown field|not defined by type|unknown argument/i.test(message)) return "query-schema";
  if (/permission|unauthori[sz]ed|access denied|authentication|forbidden/i.test(message)) return "permission";
  if (/cannot request data older|maximum.*(window|duration)|time range/i.test(message)) return "query-window";
  return "unknown";
}

async function request(endpoint, body) {
  const response = await fetch(`${api}${endpoint}`, {
    method: body ? "POST" : "GET",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    ...(body ? { body: JSON.stringify(body) } : {}),
    signal: AbortSignal.timeout(30000),
    redirect: "error",
  });
  const json = await response.json();
  if (!response.ok || json.success === false || json.errors?.length) {
    // Error messages may contain visitor data. Publish only codes and classes.
    report("api-unavailable", { endpoint, status: response.status,
      errors: (json.errors || []).map(error => ({ code: Number.isInteger(error.code) ? error.code : null, class: errorClass(error) })) });
    return null;
  }
  return json;
}

for (const [profile, userAgent] of [
  ["node-default", undefined],
  ["desktop-updater-current", "Reasonix-Updater/v1.38.11 (linux/amd64; build=stable; update=stable)"],
  ["desktop-updater-target", "Reasonix-Updater/v1.38.12 (linux/amd64; build=stable; update=stable)"],
]) {
  const probe = await fetch("https://dl.reasonix.io/latest/latest.json", {
    headers: userAgent ? { "User-Agent": userAgent } : undefined,
    signal: AbortSignal.timeout(30000), redirect: "error",
  });
  report("public-manifest", {
    profile,
    status: probe.status,
    mitigation: probe.headers.get("cf-mitigated"),
    ray: probe.headers.get("cf-ray"),
    contentType: probe.headers.get("content-type"),
  });
  await probe.body?.cancel();
}

const zones = await request("/zones?name=reasonix.io&status=active");
const matches = zones?.result?.filter(zone => zone.name === "reasonix.io");
if (matches?.length !== 1) {
  throw new Error("Cannot identify reasonix.io with this token; Zone Read permission is required");
}
const zoneID = matches[0].id;
report("zone", { name: "reasonix.io", id: zoneID });

for (const phase of ["http_config_settings", "http_request_firewall_custom", "http_request_firewall_managed", "http_request_sbfm"]) {
  const response = await request(`/zones/${zoneID}/rulesets/phases/${phase}/entrypoint`);
  if (!response) continue;
  const ruleset = response.result;
  report("ruleset", {
    phase, id: ruleset.id,
    rules: (ruleset.rules || []).map(rule => ({
      id: rule.id, action: rule.action, enabled: rule.enabled !== false,
      expressionSHA256: createHash("sha256").update(rule.expression || "").digest("hex"),
    })),
  });
}
for (const setting of ["security_level", "browser_check"]) {
  const response = await request(`/zones/${zoneID}/settings/${setting}`);
  if (response) report("setting", { setting, value: response.result.value, editable: response.result.editable });
}
const bots = await request(`/zones/${zoneID}/bot_management`);
if (bots) report("bot-management", {
  fightMode: bots.result.fight_mode,
  definitelyAutomated: bots.result.sbfm_definitely_automated,
  likelyAutomated: bots.result.sbfm_likely_automated,
});

const events = await request("/graphql", {
  query: `query ManifestChallenges($zoneTag: string, $filter: FirewallEventsAdaptiveFilter_InputObject) {
    viewer { zones(filter: { zoneTag: $zoneTag }) {
      firewallEventsAdaptive(filter: $filter, limit: 500, orderBy: [datetime_DESC]) {
        action source datetime clientRequestHTTPHost clientRequestPath
      }
    } }
  }`,
  variables: {
    zoneTag: zoneID,
    filter: {
      datetime_geq: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
      datetime_leq: new Date().toISOString(),
    },
  },
});
if (events) {
  const entries = events.data?.viewer?.zones?.[0]?.firewallEventsAdaptive ?? [];
  const matching = entries.filter(event => event.clientRequestHTTPHost === "dl.reasonix.io" && event.clientRequestPath === "/latest/latest.json");
  report("manifest-security-events", { inspected: entries.length,
    events: matching.map(({ action, source, datetime }) => ({ action, source, datetime })) });
}
