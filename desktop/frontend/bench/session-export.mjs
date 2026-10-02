import assert from "node:assert/strict";
import { createServer } from "vite";
import path from "node:path";
import { fileURLToPath } from "node:url";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const server = await createServer({ root, server: { host: "127.0.0.1", port: 0 } });
await server.listen();
const browser = await chromium.launch();
try {
 const page = await browser.newPage();
 const errors = [];
 page.on("pageerror", e => errors.push(e.message));
 await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/bench/session-export.html`);
 const result = await page.evaluate(async () => {
  const { renderSessionExportPages } = await import("/src/lib/sessionExport.tsx");
  const reports = [];
  for (const format of ["pdf", "image"]) {
   async function* blocks() {
    yield {kind:"markdown", text:"# 完整会话\n\nFIRST-QUESTION\n\n公式 $x^2+y^2=z^2$"};
    for(let group=0;group<3;group++) yield {kind:"code",label:"工具输出",text:Array.from({length:160},(_,i)=>`LINE-${group}-${i} `+"中文 abc ".repeat(12)).join("\n")};
    yield {kind:"markdown",text:"| key | value |\n|---|---|\n"+Array.from({length:80},(_,i)=>`| ROW-${i} | `+"table 中文 ".repeat(12)+" |").join("\n")};
    yield {kind:"markdown",text:"FINAL-ANSWER"};
   }
   let pages=0, maxSurfaces=0, maxWidth=0, rendered="";const seen=new WeakSet();
   for await(const output of renderSessionExportPages(blocks(),format)) {
    pages++; const surfaces=[...document.querySelectorAll(".session-export-page")];maxSurfaces=Math.max(maxSurfaces,surfaces.length);
    for(const surface of surfaces) {maxWidth=Math.max(maxWidth,surface.scrollWidth);if(!seen.has(surface)){seen.add(surface);rendered+=surface.textContent;}}
    if(output.width>8192 || output.height>8192 || !output.blob.size) throw new Error("Invalid raster page");
    const bitmap=await createImageBitmap(output.blob);if(bitmap.width!==output.width || bitmap.height!==output.height) throw new Error("Bad encoded dimensions");bitmap.close();
   }
   reports.push({format,pages,maxSurfaces,maxWidth,first:rendered.includes("FIRST-QUESTION"),last:rendered.includes("FINAL-ANSWER"),lines:Array.from({length:480},(_,i)=>`LINE-${Math.floor(i/160)}-${i%160} `).every(token=>rendered.includes(token)),surfacesAfter:document.querySelectorAll(".session-export-page").length});
  }
  return reports;
 });
 for(const report of result){assert.ok(report.pages>1);assert.equal(report.maxSurfaces,1);assert.equal(report.maxWidth,920);assert.ok(report.first&&report.last&&report.lines);assert.equal(report.surfacesAfter,0);}
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify(result,null,2));
} finally {await browser.close();await server.close();}
