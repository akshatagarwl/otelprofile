// Drive sigiro's dashboard in local Chrome and save screenshots.
//
//   node shoot.mjs <url-path> <out.png> [--wait=<css selector>] [--click=<css>] [--type=<css>::<text>]
//
// The dashboard needs some bearer in sessionStorage; sigiro's open mode
// ignores it.
import { chromium } from "playwright-core";

const BASE = process.env.DASH ?? "http://127.0.0.1:19999";
const [path, out, ...rest] = process.argv.slice(2);
const opts = Object.fromEntries(
  rest.map((a) => {
    const [k, ...v] = a.replace(/^--/, "").split("=");
    return [k, v.join("=")];
  })
);

const browser = await chromium.launch({
  executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  headless: true,
});
const page = await browser.newPage({ viewport: { width: 1600, height: 1300 }, deviceScaleFactor: 1 });
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
if (process.env.INJECT_TOKEN) await page.addInitScript(() => {
  sessionStorage.setItem(
    "sigiro.dashboard.token",
    JSON.stringify({ accessToken: "local.dev.token", expiresAt: Date.now() + 3_600_000 })
  );
});
await page.goto(BASE + path, { waitUntil: "networkidle" });
if (opts.wait) await page.waitForSelector(opts.wait, { timeout: 20000 });
if (opts.click) {
  for (const sel of opts.click.split("|")) {
    await page.click(sel);
    await page.waitForTimeout(800);
  }
}
if (opts.wait2) await page.waitForSelector(opts.wait2, { timeout: 20000 });
if (opts.type) {
  const [sel, text] = opts.type.split("::");
  await page.fill(sel, text);
  await page.waitForTimeout(800);
}
await page.waitForTimeout(Number(opts.settle ?? 1500));
await page.screenshot({ path: out, fullPage: opts.full !== "false" });
const info = await page.evaluate(() => {
  const fg = document.querySelector(".sigiro-flamegraph");
  const tbl = document.querySelector('[data-table="top-functions"]');
  return {
    title: document.title,
    flamegraphTotal: fg?.getAttribute("data-flamegraph-total"),
    flamegraphFrames: fg?.getAttribute("data-flamegraph-frames"),
    rects: document.querySelectorAll(".sigiro-flamegraph rect").length,
    topFunctions: tbl?.getAttribute("data-rows"),
    firstFunction: document.querySelector("[data-function]")?.getAttribute("data-function"),
    stats: Object.fromEntries(
      [...document.querySelectorAll("[data-stat]")].map((e) => [e.getAttribute("data-stat"), e.querySelector('[data-slot="card-title"]')?.textContent])
    ),
    totalNote: document.querySelector('[data-stat="total"] p')?.textContent,
    filters: [...document.querySelectorAll("[data-label-filter],[data-trace-filter]")].map((e) => e.textContent),
    traceProfiles: document.querySelector("[data-trace-profiles]")?.getAttribute("data-trace-profiles"),
    url: location.href,
  };
});
console.log(JSON.stringify({ ...info, errors }, null, 2));
await browser.close();
