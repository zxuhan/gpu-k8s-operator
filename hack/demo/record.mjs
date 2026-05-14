// Split-frame README hero recorder.
//
// Layout (1600 x 800 viewport):
//   left  540px  dark sidebar with two live terminals
//                  $ kubectl get gwb -n demo
//                  $ kubectl get pods -n gpu-k8s-operator-system
//   right 1060px Grafana dashboard in kiosk mode
//
// Three phases:
//   1. Hidden pre-login to Grafana, save storageState. Pre-warm the
//      dashboard so its JS chunks are cached.
//   2. Launch workload via gwb-workload and wait 15s so metrics ramp
//      before the recording context opens.
//   3. Recording context: navigate to the dashboard, inject the sidebar
//      and an annotation div, poll kubectl on a ~1.5s tick, kill the
//      operator at ~one-third in, flash the annotation, record until
//      RECORD_SECONDS.
//
// Env:
//   GRAFANA_URL         default http://localhost:3000
//   GRAFANA_USER        default admin
//   GRAFANA_PASS        default prom-operator
//   GRAFANA_DASH_UID    default gwb-demo
//   RECORD_SECONDS      default 45
//   DEMO_NAMESPACE      default demo
//   OPERATOR_NS         default gpu-k8s-operator-system
//
// Output: hack/demo/demo.webm

import { chromium } from 'playwright';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';
import { execSync } from 'child_process';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..', '..');
const outDir = path.join(here, '.record');
const stateFile = path.join(here, '.auth.json');
const grafanaUrl = process.env.GRAFANA_URL || 'http://localhost:3000';
const user = process.env.GRAFANA_USER || 'admin';
const pass = process.env.GRAFANA_PASS || 'prom-operator';
const dashUid = process.env.GRAFANA_DASH_UID || 'gwb-demo';
const durationMs = parseInt(process.env.RECORD_SECONDS || '45', 10) * 1000;
const demoNs = process.env.DEMO_NAMESPACE || 'demo';
const opNs = process.env.OPERATOR_NS || 'gpu-k8s-operator-system';

const VIEWPORT = { width: 1600, height: 800 };

fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });
fs.rmSync(stateFile, { force: true });

const browser = await chromium.launch({ headless: true });

// Helper: best-effort dismiss the "Grafana Assistant is now available" splash
// that newer Grafana versions show on first login. Sets the localStorage flag
// it uses to track dismissal, clicks any visible close button on it, and
// removes any modal whose text mentions it.
const dismissAssistantOnce = async (page) => {
  try {
    await page.evaluate(() => {
      // Flags the assistant uses across builds; set every variant we know of.
      const flags = [
        'grafana.assistant.firstTimeWelcome',
        'grafana.assistant.welcome.dismissed',
        'grafana.assistant.modal.dismissed',
        'grafana.assistant.shown',
      ];
      flags.forEach((k) => {
        try { localStorage.setItem(k, 'true'); } catch (_) {}
      });
      // Click an in-modal close if present (lots of selector variants over time).
      document.querySelectorAll('button').forEach((btn) => {
        const t = (btn.textContent || '').trim().toLowerCase();
        const a = (btn.getAttribute('aria-label') || '').toLowerCase();
        if (t === 'close' || t === 'dismiss' || a === 'close' || a.includes('close dialog')) {
          let p = btn;
          for (let i = 0; i < 8 && p; i++) {
            if (/grafana\s*assistant/i.test(p.textContent || '')) {
              btn.click();
              return;
            }
            p = p.parentElement;
          }
        }
      });
      // Hard-remove any modal whose contents mention the assistant.
      document
        .querySelectorAll('[role="dialog"], [aria-modal="true"], [class*="modal" i]')
        .forEach((el) => {
          if (/grafana\s*assistant/i.test(el.textContent || '')) el.remove();
        });
    });
  } catch (_) { /* no-op */ }
};

// ─── Phase 1: pre-login, no recording ──────────────────────────────────
{
  const ctx = await browser.newContext({ viewport: VIEWPORT });
  const page = await ctx.newPage();
  await page.goto(`${grafanaUrl}/login`, { waitUntil: 'domcontentloaded' });
  await page.fill('input[name="user"]', user);
  await page.fill('input[name="password"]', pass);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle', { timeout: 20000 }).catch(() => {});
  await page.goto(`${grafanaUrl}/d/${dashUid}?kiosk&theme=dark&refresh=2s&from=now-2m&to=now`,
                  { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(3000);
  await dismissAssistantOnce(page);
  await page.waitForTimeout(1000);
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
console.log('[record] workload launched; waiting 25s for Prometheus to scrape');
await new Promise(r => setTimeout(r, 25000));

// ─── Phase 3: recording context ────────────────────────────────────────
const ctx = await browser.newContext({
  viewport: VIEWPORT,
  storageState: stateFile,
  recordVideo: { dir: outDir, size: VIEWPORT },
});
const page = await ctx.newPage();
await page.goto(`${grafanaUrl}/d/${dashUid}?kiosk&theme=dark&refresh=2s&from=now-2m&to=now`,
                { waitUntil: 'domcontentloaded', timeout: 30000 });
await page.waitForTimeout(2000);
await dismissAssistantOnce(page);

// Re-arm: if the assistant pops up again mid-recording, evict it.
await page.evaluate(() => {
  const sweep = () => {
    document
      .querySelectorAll('[role="dialog"], [aria-modal="true"], [class*="modal" i]')
      .forEach((el) => {
        if (/grafana\s*assistant/i.test(el.textContent || '')) el.remove();
      });
  };
  sweep();
  const obs = new MutationObserver(sweep);
  obs.observe(document.body, { childList: true, subtree: true });
});

// Inject the sidebar and annotation.
await page.addStyleTag({
  content: `
    body { padding-left: 540px !important; }
    /* Defensively hide the Grafana Assistant welcome modal in case its
       markup variant isn't caught by the JS sweep. Matches any element
       whose aria-labelledby or aria-label references the assistant. */
    [aria-label*="Grafana Assistant" i],
    [aria-labelledby*="assistant" i],
    [data-testid*="assistant" i] {
      display: none !important;
    }
    /* Hide Grafana's left mega-menu and its toggle in case kiosk mode
       leaves them in the DOM. */
    nav[aria-label="Mega menu" i],
    [data-testid="navigation-mega"],
    [data-testid*="mega-menu" i],
    [data-testid*="megamenu" i],
    button[data-testid="navigation-mega-toggle"],
    [data-testid="dock-menu-button"],
    [data-testid="returnToPrevious-button"] {
      display: none !important;
    }
    /* Hide top breadcrumb / search bar / nav side rails for a cleaner shot. */
    [data-testid="topnav-breadcrumbs"],
    [data-testid="undocked-mega-menu"],
    div[data-testid="data-testid Toolbar"],
    [data-testid="bottomBlock"] {
      display: none !important;
    }
    #__termpane {
      position: fixed; top: 0; left: 0; width: 540px; height: 800px;
      background: #0b1020; color: #e2e8f0;
      font-family: 'SF Mono', Menlo, Consolas, ui-monospace, monospace;
      padding: 22px 20px;
      border-right: 1px solid #1e293b;
      z-index: 9999;
      box-sizing: border-box;
      overflow: hidden;
    }
    #__termpane .header {
      font-size: 13px; font-weight: 700; color: #f8fafc;
      letter-spacing: 0.02em;
      margin: 0 0 16px;
    }
    #__termpane .header span { color: #38bdf8; }
    #__termpane .ttitle {
      font-size: 12px; font-weight: 600; color: #94a3b8;
      margin: 18px 0 6px;
      letter-spacing: 0.03em;
    }
    #__termpane .ttitle:first-of-type { margin-top: 0; }
    #__termpane pre {
      margin: 0; padding: 11px 13px;
      background: #020617;
      border: 1px solid #0f172a;
      border-radius: 6px;
      overflow: hidden;
      color: #e2e8f0;
      font-size: 12px;
      line-height: 1.55;
      white-space: pre;
      font-family: inherit;
    }
    #__termpane #__gwb-out { height: 195px; }
    #__termpane #__pods-out { height: 245px; }
    .__kill-annot {
      position: fixed; top: 360px; left: 580px;
      background: #fef2f2; color: #991b1b;
      padding: 14px 22px; border-radius: 12px;
      font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
      font-weight: 700; font-size: 17px;
      box-shadow: 0 12px 28px rgba(0,0,0,0.35);
      z-index: 10000;
      border: 2px solid #b91c1c;
      opacity: 0; transform: translateY(-8px);
      transition: opacity 0.25s ease-out, transform 0.25s ease-out;
      pointer-events: none;
      display: flex; align-items: center; gap: 10px;
    }
    .__kill-annot::before { content: "\\2715"; font-size: 18px; }
    .__kill-annot.show { opacity: 1; transform: translateY(0); }
  `,
});

await page.evaluate(() => {
  const pane = document.createElement('div');
  pane.id = '__termpane';
  pane.innerHTML = `
    <div class="header"><span>gpu-k8s-operator</span> · live restart demo</div>
    <div class="ttitle">$ kubectl get gwb -n demo</div>
    <pre id="__gwb-out">(waiting for first reconcile…)</pre>
    <div class="ttitle">$ kubectl get pods -n gpu-k8s-operator-system</div>
    <pre id="__pods-out">(loading…)</pre>
  `;
  document.body.appendChild(pane);

  const annot = document.createElement('div');
  annot.className = '__kill-annot';
  annot.id = '__annot';
  annot.textContent = 'kubectl delete pod gwb-operator-…';
  document.body.appendChild(annot);
});

const runKubectl = (args) => {
  try {
    return execSync(`kubectl ${args}`, { stdio: ['ignore', 'pipe', 'ignore'], timeout: 4000 }).toString();
  } catch {
    return '';
  }
};

const tick = async () => {
  const gwb = runKubectl(`get gwb -n ${demoNs} 2>/dev/null`).trim() || '(none)';
  const podsRaw = runKubectl(
    `get pods -n ${opNs} -l app.kubernetes.io/name=gwb-operator --no-headers 2>/dev/null`,
  ).trim();

  // Format pod lines as a stable-width table.
  const podsHeader = 'NAME                                  READY  STATUS         RESTARTS  AGE';
  let podsBody = '(none)';
  if (podsRaw) {
    podsBody = podsRaw.split('\n').map((l) => {
      const p = l.split(/\s+/);
      if (p.length < 5) return l;
      // Truncate the long generated suffix so it fits 38 chars.
      const name = p[0].length > 38 ? p[0].slice(0, 36) + '…' : p[0];
      return [
        name.padEnd(38),
        p[1].padEnd(6),
        p[2].padEnd(14),
        p[3].padEnd(9),
        p[4],
      ].join(' ');
    }).join('\n');
  }
  const podsBlock = `${podsHeader}\n${podsBody}`;

  await page.evaluate(
    ({ g, p }) => {
      const ge = document.getElementById('__gwb-out');
      const pe = document.getElementById('__pods-out');
      if (ge) ge.textContent = g;
      if (pe) pe.textContent = p;
    },
    { g: gwb, p: podsBlock },
  );
};

await tick();
const poller = setInterval(() => { tick().catch(() => {}); }, 1500);

// Schedule the kill at ~one-third in: 15s of climb, then the restart blip,
// then ~30s of "tracked pods held" after.
const killAt = Math.max(8000, Math.floor(durationMs / 3));
const killTimer = setTimeout(async () => {
  console.log('[record] flashing annotation + killing operator pod');
  try {
    await page.evaluate(() => {
      const a = document.getElementById('__annot');
      if (a) a.classList.add('show');
    });
  } catch {}
  try {
    execSync(
      `kubectl delete pod -n ${opNs} -l app.kubernetes.io/name=gwb-operator --wait=false`,
      { stdio: 'inherit' },
    );
  } catch (e) {
    console.warn('[record] kubectl delete failed:', e.message);
  }
  setTimeout(async () => {
    try {
      await page.evaluate(() => {
        const a = document.getElementById('__annot');
        if (a) a.classList.remove('show');
      });
    } catch {}
  }, 5500);
}, killAt);

console.log(`[record] recording for ${durationMs / 1000}s`);
await page.waitForTimeout(durationMs);
clearTimeout(killTimer);
clearInterval(poller);

await ctx.close();
await browser.close();

const files = fs.readdirSync(outDir).filter((f) => f.endsWith('.webm'));
if (!files.length) {
  console.error('no webm produced in', outDir);
  process.exit(1);
}
const src = path.join(outDir, files[0]);
const dst = path.join(here, 'demo.webm');
fs.renameSync(src, dst);
fs.rmSync(stateFile, { force: true });
console.log(`recorded ${dst}`);
