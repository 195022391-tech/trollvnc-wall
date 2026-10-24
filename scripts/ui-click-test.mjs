/**
 * Playwright 自动化点击：打开墙页 → 登记 → 焦点 → 高清 → 预览 → 截图
 */
import { chromium } from "playwright";
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.WALL_URL || "http://127.0.0.1:18082";
const outDir = path.join(__dirname, "..", "test-artifacts");
fs.mkdirSync(outDir, { recursive: true });

const log = [];
function step(msg) {
  const line = `[${new Date().toISOString()}] ${msg}`;
  console.log(line);
  log.push(line);
}

async function api(pathname, body) {
  const res = await fetch(BASE + pathname, {
    method: body ? "POST" : "GET",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    data = { raw: text };
  }
  return { ok: res.ok, status: res.status, data };
}

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
page.on("dialog", async (d) => {
  step(`alert: ${d.message()}`);
  await d.accept();
});

const results = { steps: [], failed: [] };

try {
  step("1 navigate");
  // SSE 长连接会导致 networkidle 永不触发
  await page.goto(BASE + "/", { waitUntil: "domcontentloaded", timeout: 20000 });
  await page.waitForSelector("#grid", { timeout: 10000 });
  await page.waitForTimeout(2000);
  await page.screenshot({ path: path.join(outDir, "01-home.png"), fullPage: true });
  results.steps.push({ name: "navigate", ok: true });

  step("2 ensure device via API then refresh UI");
  await api("/api/devices/register", {
    deviceId: "phone-demo",
    token: "demo-token",
    name: "DemoPhone",
  });
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForSelector("#grid", { timeout: 10000 });
  await page.waitForTimeout(2500);
  await page.screenshot({ path: path.join(outDir, "02-after-load.png"), fullPage: true });

  const cells = page.locator(".cell");
  const n = await cells.count();
  step(`cells=${n}`);
  if (n < 1) {
    results.failed.push("no cells rendered");
  } else {
    results.steps.push({ name: "cells_visible", ok: true, count: n });
  }

  step("3 click 登记设备 dialog open/cancel");
  await page.click("#btn-reg");
  await page.waitForSelector("#dlg[open], dialog#dlg", { timeout: 3000 }).catch(() => {});
  const dlgOpen = await page.locator("#dlg").evaluate((el) => el.open);
  step(`dialog open=${dlgOpen}`);
  await page.screenshot({ path: path.join(outDir, "03-dialog.png") });
  // fill + submit new device
  await page.fill('input[name="deviceId"]', "ui-click-phone");
  await page.fill('input[name="token"]', "ui-click-token");
  await page.fill('input[name="name"]', "UIClickPhone");
  await page.click("#reg-ok");
  await page.waitForTimeout(1000);
  results.steps.push({ name: "register_dialog", ok: true });

  step("4 click 焦点 on first online-ish cell");
  const focusBtn = page.locator(".cell .focus").first();
  if (await focusBtn.count()) {
    await focusBtn.click();
    await page.waitForTimeout(800);
    results.steps.push({ name: "click_focus", ok: true });
  } else {
    results.failed.push("no focus button");
  }
  await page.screenshot({ path: path.join(outDir, "04-focus.png"), fullPage: true });

  step("5 click 高清");
  const hdBtn = page.locator(".cell .hd").first();
  if (await hdBtn.count()) {
    await hdBtn.click();
    await page.waitForTimeout(1500);
    results.steps.push({ name: "click_hd", ok: true });
  } else {
    results.failed.push("no hd button");
  }
  await page.screenshot({ path: path.join(outDir, "05-hd.png"), fullPage: true });

  // verify API state
  const devices = await api("/api/devices");
  const demo = (devices.data.devices || []).find((d) => d.deviceId === "phone-demo");
  step(`phone-demo uiState=${demo?.uiState} switching=${demo?.isSwitching} focused=${demo?.isFocused}`);
  results.steps.push({
    name: "hd_state",
    ok: !!demo,
    uiState: demo?.uiState,
    isSwitching: demo?.isSwitching,
    isFocused: demo?.isFocused,
  });

  step("6 click 预览");
  const prevBtn = page.locator(".cell .prev").first();
  if (await prevBtn.count()) {
    await prevBtn.click();
    await page.waitForTimeout(1000);
    results.steps.push({ name: "click_preview", ok: true });
  }
  await page.screenshot({ path: path.join(outDir, "06-preview.png"), fullPage: true });

  step("7 metrics text");
  const metrics = await page.locator("#metrics").innerText();
  step(`metrics="${metrics}"`);
  results.steps.push({ name: "metrics", ok: metrics.length > 0, text: metrics });

  // video element present
  const videos = await page.locator("video").count();
  step(`video elements=${videos}`);
  results.steps.push({ name: "video_elements", ok: videos > 0, count: videos });
} catch (e) {
  step(`FATAL ${e.stack || e}`);
  results.failed.push(String(e.message || e));
  await page.screenshot({ path: path.join(outDir, "99-error.png"), fullPage: true }).catch(() => {});
} finally {
  await browser.close();
}

const summary = {
  ok: results.failed.length === 0,
  failed: results.failed,
  steps: results.steps,
  log,
};
fs.writeFileSync(path.join(outDir, "ui-click-report.json"), JSON.stringify(summary, null, 2));
console.log("\n=== SUMMARY ===");
console.log(JSON.stringify(summary, null, 2));
process.exit(summary.ok ? 0 : 1);
