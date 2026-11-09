// Screenshot Grafana Pyroscope's built-in UI (2.4.x): pick a service and a
// profile type in the header picker, optionally replace the label selector,
// and capture the flame graph over the UI's default "Last 1h" range.
//
//   node pyroscope.mjs <service> <group/type> <out.png> [--query=<label selector>]
//        [--view=Flame Graph|Top Table|Both] [--search=<text>] [--width=1500] [--height=900]
//        [--clip=<y0>:<y1>] [--theme=Light] [--hidechart=1] [--picker=1] [--expand=1] [--scale=2]
//
// e.g. node pyroscope.mjs demo process_cpu/cpu /tmp/a.png
// PYRO defaults to http://127.0.0.1:4040.
import { chromium } from "playwright-core";

const BASE = process.env.PYRO ?? "http://127.0.0.1:4040";
const [service, type, out, ...rest] = process.argv.slice(2);
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
const width = Number(opts.width ?? 1500);
const page = await browser.newPage({ viewport: { width, height: Number(opts.height ?? 900) }, deviceScaleFactor: Number(opts.scale ?? 1) });
await page.goto(BASE + "/", { waitUntil: "networkidle" });
await page.waitForTimeout(1500);
const pause = (ms = 700) => page.waitForTimeout(ms);

// theme
const theme = opts.theme ?? "Light";
for (const t of ["Dark", "Light"]) {
  if (t !== theme && (await page.getByRole("button", { name: t, exact: true }).count())) {
    await page.getByRole("button", { name: t, exact: true }).click();
    await pause();
    await page.getByText(theme, { exact: true }).last().click();
    await pause();
  }
}

// service + profile type
await page.locator("button.btn").first().click();
await pause();
await page.getByText(service, { exact: true }).first().hover();
await page.getByText(service, { exact: true }).first().click();
await pause();
const [group, name] = type.split("/");
const box = await page.evaluate(
  ([group, name]) => {
    const header = [...document.querySelectorAll("*")].find((e) => e.children.length === 0 && e.textContent.trim().toLowerCase() === "profile type");
    let col = header;
    while (col && !col.textContent.includes(group)) col = col.parentElement;
    const leaves = [...col.querySelectorAll("*")].filter((e) => e.children.length === 0);
    const gi = leaves.findIndex((e) => e.textContent.trim() === group);
    const hit = leaves.slice(gi + 1).find((e) => e.textContent.trim() === name);
    const r = hit.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  },
  [group, name]
);
await page.mouse.click(box.x, box.y);
await pause(1500);

if (opts.query) {
  const q = page.locator("header ~ * input, input").first();
  await q.fill(opts.query);
  await page.getByRole("button", { name: "Run" }).click();
  await pause(2500);
}
await page.locator(`input[aria-label="${opts.view ?? "Flame Graph"}"]`).first().check({ force: true });
await pause();
if (opts.expand) {
  await page.getByTitle("Expand all groups").click();
  await pause(1000);
}
if (opts.search) {
  await page.getByPlaceholder("Search...").fill(opts.search);
  await pause(1000);
}
await pause(1500);
if (opts.hidechart) {
  // Leave out the "CPU cores" time series panel; keep the flame graph.
  await page.evaluate(() => {
    const t = [...document.querySelectorAll("*")].find((e) => e.children.length === 0 && /^cpu cores$/i.test(e.textContent.trim()));
    let p = t;
    while (p && p.parentElement && !p.parentElement.textContent.includes("FLAMEGRAPH") && !/flamegraph/i.test(p.parentElement.textContent)) p = p.parentElement;
    if (p) p.style.display = "none";
  });
  await pause(800);
}
if (opts.picker) {
  // Re-open the service/type picker on this service to show its profile types.
  await page.locator("button.btn").first().click();
  await pause();
  await page.getByText(service, { exact: true }).first().hover();
  await pause(1000);
}
const shot = { path: out };
if (opts.clip) {
  const [y0, y1] = opts.clip.split(":").map(Number);
  shot.clip = { x: 0, y: y0, width, height: y1 - y0 };
}
await page.screenshot(shot);
const meta = await page.evaluate(() => ({
  picker: document.querySelector("button.btn")?.textContent,
  total: [...document.querySelectorAll("*")].find((e) => e.children.length === 0 && /samples \(/.test(e.textContent))?.textContent,
}));
console.log(JSON.stringify({ service, type, out, ...meta }));
await browser.close();
