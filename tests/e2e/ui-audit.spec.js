const http = require('node:http');
const { test, expect } = require('@playwright/test');

const baseURL = process.env.RELAY_UI_URL || 'http://127.0.0.1:7717/';
const outDir = process.env.RELAY_E2E_OUTPUT || 'test-results/ui-audit';

const viewports = [
  { name: 'wide', width: 1440, height: 900 },
  { name: 'default', width: 1280, height: 820 },
  { name: 'compact', width: 1024, height: 760 },
  { name: 'narrow-desktop', width: 820, height: 720 },
];

async function audit(page, name) {
  await page.waitForTimeout(250);
  const metrics = await page.evaluate(() => {
    const selectors = [
      '#app', '#rail', '#side', '#sideList', '#main', '#topbar', '#content',
      '#work', '#meta', '#statusbar', '#respCard', '.tabBody', '.hdrWrap',
      '#respBody', '#timingBars', '.tmSecBody', '.tmPanel', '.modal-box',
    ];
    const seen = new Set();
    const boxes = [];
    const add = (el, selector) => {
      if (!el || seen.has(el)) return;
      seen.add(el);
      const style = getComputedStyle(el);
      const rect = el.getBoundingClientRect();
      const overX = el.scrollWidth > el.clientWidth + 1;
      const overY = el.scrollHeight > el.clientHeight + 1;
      boxes.push({
        selector,
        id: el.id || '',
        className: String(el.className || ''),
        rect: {
          left: Math.round(rect.left),
          top: Math.round(rect.top),
          right: Math.round(rect.right),
          bottom: Math.round(rect.bottom),
          width: Math.round(rect.width),
          height: Math.round(rect.height),
        },
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
        scrollHeight: el.scrollHeight,
        clientHeight: el.clientHeight,
        overflowX: style.overflowX,
        overflowY: style.overflowY,
        overX,
        overY,
        badX: overX && !['auto', 'scroll'].includes(style.overflowX),
        badY: overY && !['auto', 'scroll'].includes(style.overflowY),
      });
    };
    for (const selector of selectors) {
      document.querySelectorAll(selector).forEach(el => add(el, selector));
    }
    const viewportLeaks = [];
    for (const el of document.querySelectorAll('#rail,#side,#main,#topbar,#content,#work,#meta,#statusbar,.card,.tmPanel')) {
      const rect = el.getBoundingClientRect();
      if (rect.width < 1 || rect.height < 1) continue;
      if (rect.left < -2 || rect.right > innerWidth + 2) {
        viewportLeaks.push({
          id: el.id || '',
          className: String(el.className || ''),
          left: Math.round(rect.left),
          right: Math.round(rect.right),
          width: Math.round(rect.width),
        });
      }
    }
    return {
      title: document.title,
      width: innerWidth,
      height: innerHeight,
      docScrollWidth: document.documentElement.scrollWidth,
      bodyScrollWidth: document.body.scrollWidth,
      boxes,
      badOverflow: boxes.filter(b => b.badX || b.badY),
      viewportLeaks,
    };
  });
  console.log('AUDIT ' + name + ' ' + JSON.stringify(metrics));
  expect(metrics.title).toContain('Relay');
  expect(metrics.docScrollWidth, `${name} document width`).toBeLessThanOrEqual(metrics.width + 2);
  expect(metrics.badOverflow, `${name} clipped elements`).toEqual([]);
  expect(metrics.viewportLeaks, `${name} viewport leaks`).toEqual([]);
  await page.screenshot({ path: `${outDir}/${name}.png`, fullPage: true });
}

test.describe.configure({ mode: 'serial' });

let fixtureServer;

test.beforeAll(async () => {
  fixtureServer = http.createServer((request, response) => {
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({ ok: true, path: request.url }));
  });
  await new Promise((resolve, reject) => {
    fixtureServer.once('error', reject);
    fixtureServer.listen(18080, '127.0.0.1', resolve);
  });
});

test.afterAll(async () => {
  if (fixtureServer) {
    await new Promise((resolve, reject) => fixtureServer.close(error => error ? reject(error) : resolve()));
  }
});

test('Relay desktop UI audit', async ({ page }) => {
  for (const vp of viewports) {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    await page.goto(baseURL, { waitUntil: 'domcontentloaded' });
    await audit(page, `collections-${vp.name}`);
  }

  await page.setViewportSize({ width: 1280, height: 820 });
  await page.goto(baseURL, { waitUntil: 'domcontentloaded' });
  const splitter = page.getByRole('separator', { name: 'Resize request and response panes' });
  await expect(splitter).toBeVisible();
  const responseBeforeResize = await page.locator('#respCard').boundingBox();
  const splitterBox = await splitter.boundingBox();
  expect(responseBeforeResize).not.toBeNull();
  expect(splitterBox).not.toBeNull();
  await page.mouse.move(splitterBox.x + splitterBox.width / 2, splitterBox.y + splitterBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(splitterBox.x + splitterBox.width / 2, splitterBox.y - 80);
  await page.mouse.up();
  const responseAfterResize = await page.locator('#respCard').boundingBox();
  expect(responseAfterResize).not.toBeNull();
  expect(responseAfterResize.height).toBeGreaterThan(responseBeforeResize.height + 60);
  const envOptions = await page.locator('#envSel option').allTextContents();
  if (envOptions.length > 1) {
    await page.locator('#envSel').selectOption({ index: 1 });
  }
  const requestTabs = () => page.locator('#work > .card').first().locator('.tabs button');
  const responseTabs = () => page.locator('#respCard .tabs button');

  await page.getByText('Verify Individual').click();
  await requestTabs().filter({ hasText: /^Body$/ }).click();
  await audit(page, 'request-body-default');

  await requestTabs().filter({ hasText: /^Scripts/ }).click();
  await audit(page, 'request-scripts-default');

  await page.getByText('Health').click();
  await page.getByRole('button', { name: 'Send' }).click();
  await page.waitForTimeout(1200);
  if (await responseTabs().filter({ hasText: /^Body$/ }).count()) {
    await responseTabs().filter({ hasText: /^Body$/ }).click();
  }
  await audit(page, 'response-body-default');

  if (await responseTabs().filter({ hasText: /^Headers/ }).count()) {
    await responseTabs().filter({ hasText: /^Headers/ }).click();
  }
  await audit(page, 'response-headers-default');

  await page.getByText('Test Management').click();
  await page.waitForTimeout(900);
  await audit(page, 'tests-default');

  await page.setViewportSize({ width: 1024, height: 760 });
  await audit(page, 'tests-compact');
});
