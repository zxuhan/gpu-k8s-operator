// Records the Grafana dashboard at localhost:3000 while orchestrate.sh
// drives the cluster. Playwright captures a 1280x720 webm; the Makefile
// converts that to docs/media/demo.gif.
//
// Env:
//   GRAFANA_URL         default http://localhost:3000
//   GRAFANA_USER        default admin
//   GRAFANA_PASS        default prom-operator (kube-prometheus-stack default)
//   GRAFANA_DASH_UID    default gwb-demo
//   RECORD_SECONDS      default 90
//
// Output: hack/demo/demo.webm

import { chromium } from 'playwright';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const here = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.join(here, '.record');
const grafanaUrl = process.env.GRAFANA_URL || 'http://localhost:3000';
const user = process.env.GRAFANA_USER || 'admin';
const pass = process.env.GRAFANA_PASS || 'prom-operator';
const dashUid = process.env.GRAFANA_DASH_UID || 'gwb-demo';
const durationMs = parseInt(process.env.RECORD_SECONDS || '90', 10) * 1000;

fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({
  viewport: { width: 1280, height: 720 },
  recordVideo: { dir: outDir, size: { width: 1280, height: 720 } },
});
const page = await ctx.newPage();

async function login() {
  await page.goto(`${grafanaUrl}/login`, { waitUntil: 'domcontentloaded' });
  await page.fill('input[name="user"]', user);
  await page.fill('input[name="password"]', pass);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle', { timeout: 20000 }).catch(() => {});
}

await login();

// Kiosk mode hides chrome for a clean recording; theme=dark forces
// dark mode even if the user profile hasn't been set yet.
const dashUrl = `${grafanaUrl}/d/${dashUid}?kiosk=tv&refresh=2s&from=now-2m&to=now&theme=dark`;
await page.goto(dashUrl, { waitUntil: 'networkidle', timeout: 30000 });
// Give React time to render panels (Grafana selectors shift between versions).
await page.waitForTimeout(5000);

console.log(`recording for ${durationMs / 1000}s`);
await page.waitForTimeout(durationMs);

await ctx.close();
await browser.close();

const files = fs.readdirSync(outDir).filter(f => f.endsWith('.webm'));
if (!files.length) {
  console.error('no webm produced in', outDir);
  process.exit(1);
}
const src = path.join(outDir, files[0]);
const dst = path.join(here, 'demo.webm');
fs.renameSync(src, dst);
console.log(`recorded ${dst}`);
