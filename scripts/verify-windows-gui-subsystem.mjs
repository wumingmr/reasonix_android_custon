#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const WINDOWS_GUI_SUBSYSTEM = 2;

export function readPESubsystem(bytes) {
  const image = Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes);
  if (image.length < 0x40 || image.toString("ascii", 0, 2) !== "MZ") {
    throw new Error("not a valid DOS/PE image");
  }

  const peOffset = image.readUInt32LE(0x3c);
  const optionalHeaderOffset = peOffset + 24;
  const subsystemOffset = optionalHeaderOffset + 68;
  if (
    peOffset > image.length - 24 ||
    image.toString("binary", peOffset, peOffset + 4) !== "PE\0\0" ||
    subsystemOffset > image.length - 2
  ) {
    throw new Error("not a valid PE image");
  }

  const optionalHeaderSize = image.readUInt16LE(peOffset + 20);
  if (optionalHeaderSize < 70 || optionalHeaderOffset + optionalHeaderSize > image.length) {
    throw new Error("invalid PE optional header");
  }
  const magic = image.readUInt16LE(optionalHeaderOffset);
  if (magic !== 0x10b && magic !== 0x20b) {
    throw new Error(`unsupported PE optional header magic 0x${magic.toString(16)}`);
  }
  return image.readUInt16LE(subsystemOffset);
}

export function verifyWindowsGUISubsystem(file) {
  const subsystem = readPESubsystem(fs.readFileSync(file));
  if (subsystem !== WINDOWS_GUI_SUBSYSTEM) {
    throw new Error(
      `${file}: expected Windows GUI subsystem ${WINDOWS_GUI_SUBSYSTEM}, got ${subsystem}`,
    );
  }
}

function main(files) {
  if (files.length === 0) {
    throw new Error("usage: verify-windows-gui-subsystem.mjs <exe> [exe ...]");
  }
  for (const file of files) {
    verifyWindowsGUISubsystem(file);
    console.log(`${file}: Windows GUI subsystem verified`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2));
  } catch (error) {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  }
}
