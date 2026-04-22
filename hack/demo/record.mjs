// Records the Grafana dashboard at localhost:3000. Two-phase flow so
// the first frame the viewer sees is the live dashboard, not a login
// redirect or a Grafana home page:
//
//   1. Hidden browser context (no recording). Log in, fetch cookies, save
//      storageState. Also pre-warms the dashboard so Grafana caches its
//      layout.
//   2. Launch the workload via kubectl so metrics ramp for ~15s before
//      any recording starts. The gauge is already climbing by the time
//      the recorder starts.
//   3. Recording context with storageState restored. Navigate straight
//      to the dashboard URL. Record for RECORD_SECONDS. At the 20s mark
//      of the recording, kill the operator pod.
//
// Env:
//   GRAFANA_URL         default http://localhost:3000
//   GRAFANA_USER        default admin
//   GRAFANA_PASS        default prom-operator
//   GRAFANA_DASH_UID    default gwb-demo
//   RECORD_SECONDS      default 60
//   DEMO_NAMESPACE      default demo
//   OPERATOR_NS         default gpu-k8s-operator-system
//
// Output: hack/demo/demo.webm

import { chromium } from 'playwright';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import { execSync, spawn } from 'child_process';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..', '..');
const outDir = path.join(here, '.record');
const stateFile = path.join(here, '.auth.json');
const grafanaUrl = process.env.GRAFANA_URL || 'http://localhost:3000';
const user = process.env.GRAFANA_USER || 'admin';
const pass = process.env.GRAFANA_PASS || 'prom-operator';
const dashUid = process.env.GRAFANA_DASH_UID || 'gwb-demo';
const durationMs = parseInt(process.env.RECORD_SECONDS || '60', 10) * 1000;
const demoNs = process.env.DEMO_NAMESPACE || 'demo';
const opNs = process.env.OPERATOR_NS || 'gpu-k8s-operator-system';

fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });
fs.rmSync(stateFile, { force: true });

const browser = await chromium.launch({ headless: true });

// ─── Phase 1: pre-login, no recording ──────────────────────────────────
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 } });
  const page = await ctx.newPage();
  await page.goto(`${grafanaUrl}/login`, { waitUntil: 'domcontentloaded' });
  await page.fill('input[name="user"]', user);
  await page.fill('input[name="password"]', pass);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle', { timeout: 20000 }).catch(() => {});
  // Pre-warm the dashboard so its JS chunks are cached.
  await page.goto(`${grafanaUrl}/d/${dashUid}?kiosk=tv&theme=dark&refresh=2s&from=now-2m&to=now`,
                  { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(2000);
  await ctx.storageState({ path: stateFile });
  await ctx.close();
}

// ─── Phase 2: launch workload so metrics ramp before recording ─────────
console.log('[record] launching workload');
execSync(
  `${path.join(root, 'bin', 'gwb-workload')} --namespace=${demoNs} --label=app=demo ` +
  `--count=8 --rate=2 --runtime=120s --gpus=100m --gpu-resource=cpu >/dev/null`,
  { stdio: 'inherit', shell: '/bin/bash' }
);
console.log('[record] workload launched; waiting 15s for gauge to ramp');
await new Promise(r => setTimeout(r, 15000));

// ─── Phase 3: recording context ────────────────────────────────────────
const ctx = await browser.newContext({
  viewport: { width: 1280, height: 720 },
  storageState: stateFile,
  recordVideo: { dir: outDir, size: { width: 1280, height: 720 } },
});
const page = await ctx.newPage();
await page.goto(`${grafanaUrl}/d/${dashUid}?kiosk=tv&theme=dark&refresh=2s&from=now-2m&to=now`,
                { waitUntil: 'domcontentloaded', timeout: 30000 });
// Give React a beat to paint populated panels (storageState short-circuits
// the login redirect, so panels hydrate with data immediately).
await page.waitForTimeout(1500);

// Schedule the operator kill at the 20s mark of the recording. That
// gives 20s of "gauge climbing past quota", then the restart blip,
// then ~20s of "tracked pods held at 8".
const killAt = Math.max(1000, durationMs / 3);
const killTimer = setTimeout(() => {
  console.log('[record] killing operator pod');
  spawn('kubectl', [
    'delete', 'pod', '-n', opNs,
    '-l', 'app.kubernetes.io/name=gwb-operator',
    '--wait=false',
  ], { stdio: 'inherit' });
}, killAt);

console.log(`[record] recording for ${durationMs / 1000}s`);
await page.waitForTimeout(durationMs);
clearTimeout(killTimer);

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
fs.rmSync(stateFile, { force: true });
console.log(`recorded ${dst}`);
