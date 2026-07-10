// Capture the docs-site screenshots and flow GIFs from a LIVE stack.
//
// The dashboard JWT is held in memory only (never localStorage — a security
// choice in the frontend), so a full page reload logs out. The script therefore
// logs in once through the form and then navigates by clicking in-app <a href>
// links (TanStack Router = client-side nav, token preserved) rather than
// page.goto between authenticated pages.
//
// Prerequisites:
//   1. The dev stack is up (docker compose up -d) and the frontend is reachable
//      at $JANUS_BASE_URL (default http://localhost:3000).
//   2. `npm install` here, and `npx playwright install chromium`.
//   3. ffmpeg on PATH (for the webm → GIF conversion).
//
// Usage:
//   node capture.mjs                 # screenshots + GIFs → ../../docs/assets
//   ONLY=shots node capture.mjs      # screenshots only
//   ONLY=gifs  node capture.mjs      # GIFs only
//
// Config via env (dev defaults are the public docker-compose creds):
//   JANUS_BASE_URL, JANUS_ADMIN_USER, JANUS_ADMIN_PASSWORD, OUT_DIR
import { chromium } from "playwright";
import { mkdir, rm } from "node:fs/promises";
import { execFileSync } from "node:child_process";

const BASE = process.env.JANUS_BASE_URL ?? "http://localhost:3000";
const USER = process.env.JANUS_ADMIN_USER ?? "admin";
const PASS = process.env.JANUS_ADMIN_PASSWORD ?? "Admin1234!";
const OUT = process.env.OUT_DIR ?? "../../docs/assets";
const ONLY = process.env.ONLY ?? ""; // "shots" | "gifs" | "" (both)

const VIEW = { width: 1360, height: 850 };
const SHOTS = `${OUT}/screenshots`;
const GIFS = `${OUT}/gifs`;

// settle waits for the SPA to finish: network quiet, then all loading
// skeletons gone (the app marks them with .skeleton-shimmer), then a beat.
async function settle(page, timeout = 9000) {
  await page.waitForLoadState("networkidle").catch(() => {});
  await page
    .waitForFunction(
      () => document.querySelectorAll(".skeleton-shimmer").length === 0,
      null,
      { timeout },
    )
    .catch(() => {});
  await page.waitForTimeout(500);
}

async function login(page) {
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle" });
  await page.fill("#username", USER);
  await page.fill("#password", PASS);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20000 }),
    page.getByRole("button", { name: /sign in/i }).click(),
  ]);
  await settle(page);
}

// clickLink navigates by an in-app <a href> (client-side nav, so the in-memory
// JWT survives — a full page.goto would log us out).
async function clickLink(page, href, { prefix = false } = {}) {
  const sel = prefix ? `a[href^="${href}"]` : `a[href="${href}"]`;
  await page.locator(sel).first().click();
  await settle(page);
}

async function shot(page, name) {
  await page.screenshot({ path: `${SHOTS}/${name}.png` });
  console.log("  shot:", name, "@", page.url());
}

async function captureShots(browser) {
  const ctx = await browser.newContext({ viewport: VIEW, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  await login(page);
  await shot(page, "dashboard");

  await clickLink(page, "/repositories");
  await shot(page, "repositories");

  // Repo detail → tag detail (needs at least one pushed image in the registry).
  await clickLink(page, "/repositories/", { prefix: true });
  await shot(page, "repository-detail");
  const tag = page.locator('a[href*="/tags/"]').first();
  if (await tag.count()) {
    await tag.click();
    await settle(page);
    await shot(page, "tag-detail");
  }

  await clickLink(page, "/security");
  await shot(page, "security");
  for (const [href, name] of [
    ["/security/vulnerabilities", "security-vulnerabilities"],
    ["/security/scans", "security-scans"],
  ]) {
    const l = page.locator(`a[href="${href}"]`).first();
    if (await l.count()) {
      await l.click();
      await settle(page);
      await shot(page, name);
    }
  }

  await clickLink(page, "/activity");
  await shot(page, "activity");

  await clickLink(page, "/webhooks");
  await shot(page, "webhooks");

  await clickLink(page, "/members");
  await shot(page, "organizations");

  await clickLink(page, "/api-keys");
  await shot(page, "api-keys");

  await clickLink(page, "/settings");
  await shot(page, "settings");
  const integ = page.locator('a[href="/settings/integrations"]').first();
  if (await integ.count()) {
    await integ.click();
    await settle(page);
    await shot(page, "settings-integrations");
  }

  await ctx.close();
}

// ── GIF flows ────────────────────────────────────────────────────────────────
const GIFVIEW = { width: 1200, height: 750 };
const pause = (p, ms) => p.waitForTimeout(ms);

// loginFast fills + submits instantly — a short authenticated lead-in for the
// non-login flows (vs the typed hero in gifLoginFlow).
async function loginFast(page) {
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle" });
  await pause(page, 500);
  await page.fill("#username", USER);
  await page.fill("#password", PASS);
  await pause(page, 300);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20000 }),
    page.getByRole("button", { name: /sign in/i }).click(),
  ]);
  await settle(page);
  await pause(page, 900);
}

async function gifLoginFlow(page) {
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle" });
  await pause(page, 900);
  await page.locator("#username").pressSequentially(USER, { delay: 85 });
  await pause(page, 350);
  await page.locator("#password").pressSequentially(PASS, { delay: 85 });
  await pause(page, 600);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20000 }),
    page.getByRole("button", { name: /sign in/i }).click(),
  ]);
  await settle(page);
  await pause(page, 2200);
}

async function gifExploreFlow(page) {
  await loginFast(page);
  await clickLink(page, "/repositories");
  await pause(page, 1300);
  await clickLink(page, "/repositories/", { prefix: true });
  await pause(page, 1500);
  const tag = page.locator('a[href*="/tags/"]').first();
  if (await tag.count()) {
    await tag.click();
    await settle(page);
    await pause(page, 2200);
  }
}

async function gifSecurityFlow(page) {
  await loginFast(page);
  await clickLink(page, "/security");
  await pause(page, 1600);
  for (const href of ["/security/vulnerabilities", "/security/scans"]) {
    const l = page.locator(`a[href="${href}"]`).first();
    if (await l.count()) {
      await l.click();
      await settle(page);
      await pause(page, 1800);
    }
  }
}

// recordGif runs a flow in a video-recording context, then converts the webm to
// an optimised GIF via ffmpeg (two-pass palette, capped colours for size).
async function recordGif(browser, name, flow) {
  const ctx = await browser.newContext({
    viewport: GIFVIEW,
    recordVideo: { dir: `${GIFS}/_video`, size: GIFVIEW },
  });
  const page = await ctx.newPage();
  await flow(page);
  const vpath = await page.video().path();
  await ctx.close(); // finalises the video file
  execFileSync(
    "ffmpeg",
    [
      "-y", "-i", vpath,
      "-vf",
      "fps=10,scale=900:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128:stats_mode=diff[p];[s1][p]paletteuse=dither=bayer:bayer_scale=3",
      "-loop", "0", `${GIFS}/${name}.gif`,
    ],
    { stdio: "ignore" },
  );
  console.log("  gif:", name);
}

async function captureGifs(browser) {
  await recordGif(browser, "login", gifLoginFlow);
  await recordGif(browser, "explore-image", gifExploreFlow);
  await recordGif(browser, "security-tour", gifSecurityFlow);
  await rm(`${GIFS}/_video`, { recursive: true, force: true });
}

async function main() {
  await mkdir(SHOTS, { recursive: true });
  await mkdir(GIFS, { recursive: true });
  const browser = await chromium.launch();
  if (ONLY !== "gifs") await captureShots(browser);
  if (ONLY !== "shots") await captureGifs(browser);
  await browser.close();
  console.log("done");
}

await main();
