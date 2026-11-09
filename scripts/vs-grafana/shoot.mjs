import { chromium } from '../../e2e/node_modules/playwright-core/index.mjs';

const [service, group, type, output] = process.argv.slice(2);
const browser = await chromium.launch({
  executablePath: process.env.CHROME ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  headless: true,
});
try {
  const page = await browser.newPage({ viewport: { width: 1500, height: 950 }, deviceScaleFactor: 1 });
  await page.goto(process.env.PYRO ?? 'http://127.0.0.1:14040', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Dark', exact: true }).click();
  await page.getByText('Light', { exact: true }).last().click();
  await page.locator('.cascade-select > button').click();
  await page.locator('.cascade-group-row').getByText(service, { exact: true }).hover();
  const index = await page.locator('.cascade-items').evaluate((element, { group, type }) => {
    let active = false;
    let item = -1;
    for (const child of element.children) {
      if (child.matches('.dropdown-section[data-subsection]')) active = child.textContent.trim() === group;
      if (child.matches('.dropdown-item')) {
        item++;
        if (active && child.textContent.trim() === type) return item;
      }
    }
    throw new Error(`missing profile ${group}/${type}`);
  }, { group, type });
  await page.locator('.cascade-items > .dropdown-item').nth(index).click();
  await page.waitForTimeout(2500);
  await page.locator('input[aria-label="Both"]').check({ force: true });
  await page.getByTitle('Expand all groups').click();
  await page.waitForTimeout(500);
  const selected = await page.locator('.cascade-select > button').textContent();
  const query = await page.locator('.querybar-input').inputValue();
  if (!selected.includes(service) || !query.includes(service)) throw new Error(`wrong service: ${selected} ${query}`);
  const text = await page.locator('body').innerText();
  if (!text.includes('main.')) throw new Error('no workload function in backend profile');
  await page.screenshot({ path: output });
  console.log(JSON.stringify({ service, group, type, selected, query, output }));
} finally {
  await browser.close();
}
