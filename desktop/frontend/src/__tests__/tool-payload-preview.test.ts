import assert from "node:assert/strict";
import { boundedPayloadSections, TOOL_PREVIEW_MAX_BLOCKS, TOOL_PREVIEW_MAX_BYTES, utf8Prefix } from "../lib/toolPayloadPreview";

const fields = Object.fromEntries(Array.from({ length: 100 }, (_, index) => [`field-${index}`, "界".repeat(1000)]));
const sections = boundedPayloadSections(fields);
assert.ok(sections.length <= TOOL_PREVIEW_MAX_BLOCKS, "preview bounds structured blocks");
assert.ok(new TextEncoder().encode(sections.map(section => section.body).join("")).byteLength <= TOOL_PREVIEW_MAX_BYTES, "preview bounds UTF-8 bytes");
assert.equal(utf8Prefix("😀😀", 4), "😀", "UTF-8 truncation preserves surrogate pairs");
console.log("tool payload preview: 16KiB and 64-block budgets passed");
