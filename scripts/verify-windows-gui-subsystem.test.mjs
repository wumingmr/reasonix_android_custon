import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  readPESubsystem,
  verifyWindowsGUISubsystem,
  WINDOWS_GUI_SUBSYSTEM,
} from "./verify-windows-gui-subsystem.mjs";

function peImage(subsystem, magic = 0x20b) {
  const image = Buffer.alloc(0x200);
  image.write("MZ", 0, "ascii");
  image.writeUInt32LE(0x80, 0x3c);
  image.write("PE\0\0", 0x80, "binary");
  image.writeUInt16LE(0xf0, 0x80 + 20);
  image.writeUInt16LE(magic, 0x80 + 24);
  image.writeUInt16LE(subsystem, 0x80 + 24 + 68);
  return image;
}

test("reads the subsystem from PE32 and PE32+ optional headers", () => {
  assert.equal(readPESubsystem(peImage(WINDOWS_GUI_SUBSYSTEM, 0x10b)), 2);
  assert.equal(readPESubsystem(peImage(WINDOWS_GUI_SUBSYSTEM, 0x20b)), 2);
});

test("rejects a console-subsystem executable", () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "reasonix-pe-subsystem-"));
  const executable = path.join(directory, "reasonix-desktop.exe");
  try {
    fs.writeFileSync(executable, peImage(3));
    assert.throws(
      () => verifyWindowsGUISubsystem(executable),
      /expected Windows GUI subsystem 2, got 3/,
    );
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test("rejects malformed and unsupported executable headers", () => {
  assert.throws(() => readPESubsystem(Buffer.from("not an executable")), /DOS\/PE/);
  assert.throws(() => readPESubsystem(peImage(2, 0x999)), /unsupported PE/);
});
