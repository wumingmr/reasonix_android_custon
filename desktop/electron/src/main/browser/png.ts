import { crc32, inflateSync } from "node:zlib";
import { browserFailure } from "./errors.js";

export const MAX_CAPTURE_PIXELS = 16_777_216;
const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);

export function pngSize(data: Buffer): { width: number; height: number } {
  if (data.length < 33 || !data.subarray(0, 8).equals(signature) || data.toString("ascii", 12, 16) !== "IHDR") {
    throw browserFailure("invalid_image", "capture did not return a PNG image");
  }
  const width = data.readUInt32BE(16), height = data.readUInt32BE(20);
  if (width === 0 || height === 0 || width * height > MAX_CAPTURE_PIXELS) {
    throw browserFailure("invalid_image", "capture has empty or excessive pixel dimensions; capture a smaller region");
  }
  return { width, height };
}

export function validatePNG(data: Buffer): { width: number; height: number } {
  const size = pngSize(data);
  const compressed: Buffer[] = [];
  let ended = false;
  for (let offset = 8; offset + 12 <= data.length;) {
    const length = data.readUInt32BE(offset);
    const end = offset + 8 + length;
    if (end + 4 > data.length || crc32(data.subarray(offset + 4, end)) !== data.readUInt32BE(end)) {
      throw browserFailure("invalid_image", "capture returned a truncated or corrupt PNG");
    }
    const kind = data.toString("ascii", offset + 4, offset + 8);
    if (kind === "IDAT") compressed.push(data.subarray(offset + 8, end));
    if (kind === "IEND") { ended = length === 0 && end + 4 === data.length; break; }
    offset = end + 4;
  }
  if (!ended || compressed.length === 0) throw browserFailure("invalid_image", "capture returned an incomplete PNG");
  try {
    if (inflateSync(Buffer.concat(compressed), { maxOutputLength: MAX_CAPTURE_PIXELS * 8 + 65536 }).length === 0) throw new Error("empty raster");
  } catch {
    throw browserFailure("invalid_image", "capture returned an undecodable PNG raster");
  }
  return size;
}
