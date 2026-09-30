const http = require('node:http');
const { test, expect } = require('@playwright/test');

const baseURL = process.env.RELAY_UI_URL || 'http://127.0.0.1:7717/';
const outDir = process.env.RELAY_E2E_OUTPUT || 'test-results/ui-audit';

const viewports = [
  { name: 'wide', width: 1440, height: 900 },
  { name: 'default', width: 1280, height: 820 },
  { name: 'compact', width: 1024, height: 760 },
  { name: 'narrow-desktop', width: 820, height: 720 },
  { name: 'minimum', width: 760, height: 480 },
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
  console.log('AUDIT ' + name + ' ' + JSON.stringify({ width: metrics.width, height: metrics.height, badOverflow: metrics.badOverflow, viewportLeaks: metrics.viewportLeaks }));
  expect(metrics.title).toContain('Relay');
  expect(metrics.docScrollWidth, `${name} document width`).toBeLessThanOrEqual(metrics.width + 2);
  expect(metrics.badOverflow, `${name} clipped elements`).toEqual([]);
  expect(metrics.viewportLeaks, `${name} viewport leaks`).toEqual([]);
  await page.screenshot({ path: `${outDir}/${name}.png`, fullPage: true });
}

let fixtureServer;
let cancelledRequests = 0;
const pageErrors = new WeakMap();

test.beforeEach(async ({ page }) => {
  const errors = [];
  pageErrors.set(page, errors);
  page.on('pageerror', error => errors.push(error.message));
});

test.afterEach(async ({ page }) => {
  expect(pageErrors.get(page), 'Uncaught browser exceptions').toEqual([]);
});

test.beforeAll(async () => {
  fixtureServer = http.createServer((request, response) => {
    if (request.url === '/slow') {
      const timer = setTimeout(() => response.end('late response'), 15000);
      response.on('close', () => {
        clearTimeout(timer);
        if (!response.writableEnded) cancelledRequests++;
      });
      return;
    }
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
  const requestTabs = () => page.getByRole('tablist', { name: 'Request editor sections' }).getByRole('tab');
  const responseTabs = () => page.locator('#respCard .tabs button');

  await page.locator('#sideList').getByText('Verify Individual', { exact: true }).click();
  await requestTabs().filter({ hasText: /^Body$/ }).click();
  await audit(page, 'request-body-default');

  await requestTabs().filter({ hasText: /^Scripts/ }).click();
  await audit(page, 'request-scripts-default');

  await page.locator('#sideList').getByText('Health', { exact: true }).click();
  await page.locator('#envSel').selectOption('');
  await page.getByRole('textbox', { name: 'Request URL', exact: true }).fill('http://127.0.0.1:18080/health');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.locator('#respMeta')).toContainText('200 OK');
  await responseTabs().filter({ hasText: /^Body$/ }).click();
  await expect(page.locator('#respBody')).toContainText('"ok": true');
  await audit(page, 'response-body-default');

  await responseTabs().filter({ hasText: /^Headers/ }).click();
  await audit(page, 'response-headers-default');

  for (const viewport of viewports) {
    await page.setViewportSize(viewport);
    await audit(page, `request-${viewport.name}`);
    const controls = await page.locator('#method,#url,#sendBtn').evaluateAll(elements => elements.map(element => {
      const rect = element.getBoundingClientRect();
      return { top: rect.top, bottom: rect.bottom, width: rect.width };
    }));
    expect(controls).toHaveLength(3);
    expect(Math.max(...controls.map(control => control.top)) - Math.min(...controls.map(control => control.top))).toBeLessThan(5);
    for (const control of controls) {
      expect(control.width).toBeGreaterThan(50);
      expect(control.bottom).toBeLessThan(viewport.height);
    }
  }
  await page.setViewportSize({ width: 1280, height: 820 });
  await page.getByRole('button', { name: 'Test management', exact: true }).click();
  await expect(page.locator('.tmRequestSummary')).toBeVisible();
  await expect(page.locator('details.tmPanel[open]')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Push execution', exact: true })).toHaveCount(0);
  const identity = page.locator('summary').filter({ hasText: 'Identity' });
  await identity.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByLabel('Test name', { exact: true })).toBeVisible();
  await identity.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByLabel('Test name', { exact: true })).toBeHidden();
  await audit(page, 'tests-default');

  await page.setViewportSize({ width: 1024, height: 760 });
  await audit(page, 'tests-compact');
  await page.setViewportSize({ width: 760, height: 480 });
  await audit(page, 'tests-minimum');
  const content = await page.locator('#content').boundingBox();
  expect(content.height).toBeGreaterThan(200);
});

test('late history responses preserve the active view', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  let releaseHistory;
  const historyReady = new Promise(resolve => { releaseHistory = resolve; });
  await page.route('**/api/history?*', async route => {
    await historyReady;
    await route.fulfill({ json: [] });
  });
  await page.getByRole('button', { name: 'History', exact: true }).click();
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.locator('#crumb')).toContainText('Settings');
  releaseHistory();
  await page.waitForLoadState('networkidle');
  await expect(page.locator('#crumb')).toContainText('Settings');
  await expect(page.getByRole('heading', { name: 'Request history' })).toHaveCount(0);
});

test('late settings responses preserve the active request', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  let releaseSettings;
  const settingsReady = new Promise(resolve => { releaseSettings = resolve; });
  await page.route('**/api/xray/settings', async route => {
    await settingsReady;
    await route.continue();
  });
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('button', { name: 'Collections', exact: true }).click();
  releaseSettings();
  await page.waitForLoadState('networkidle');
  await expect(page.getByRole('textbox', { name: 'Request URL', exact: true })).toBeVisible();
  await expect(page.locator('#crumb')).not.toContainText('Settings');
});

test('credentials are masked unless explicitly revealed', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.locator('#sideList').getByText('Verify Individual', { exact: true }).click();
  await page.getByRole('tab', { name: 'Authorization', exact: true }).click();
  await expect(page.getByLabel('Authorization type', { exact: true })).toHaveValue('bearer');
  const token = page.getByLabel('Token', { exact: true });
  await expect(token).toHaveAttribute('type', 'password');
  await page.getByRole('checkbox', { name: 'Show token', exact: true }).check();
  await expect(token).toHaveAttribute('type', 'text');
  await page.getByRole('checkbox', { name: 'Show token', exact: true }).uncheck();
  await expect(token).toHaveAttribute('type', 'password');
  await expect(page.locator('.varMirror')).not.toContainText('{{apiToken}}');
});

test('failed saves block sending and preserve the draft', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  let sends = 0;
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/send') sends++; });
  await page.route('**/api/requests/*', route => route.request().method() === 'PUT'
    ? route.fulfill({ status: 500, json: { error: 'Save unavailable in regression fixture' } })
    : route.continue());
  const url = page.getByRole('textbox', { name: 'Request URL', exact: true });
  await url.fill('http://127.0.0.1:18080/unsaved');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.locator('#retrySaveBtn')).toBeVisible();
  await expect(url).toHaveValue('http://127.0.0.1:18080/unsaved');
  expect(sends).toBe(0);
  await page.unroute('**/api/requests/*');
  await page.locator('#saveReqBtn').click();
  await expect(page.locator('#retrySaveBtn')).toBeHidden();
});

test('individual send cancellation closes the upstream request', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.locator('#envSel').selectOption('');
  const before = cancelledRequests;
  await page.getByRole('textbox', { name: 'Request URL', exact: true }).fill('http://127.0.0.1:18080/slow');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.locator('#sendBtn')).toHaveText('Sending…');
  await page.getByRole('tab', { name: 'Headers', exact: true }).first().click();
  await expect(page.getByRole('button', { name: 'Cancel request', exact: true })).toContainText('s)');
  await page.getByRole('button', { name: 'Cancel request', exact: true }).click();
  await expect(page.locator('#sendBtn')).toBeEnabled();
  await expect(page.locator('#respCard')).toContainText('Request cancelled');
  await expect.poll(() => cancelledRequests).toBe(before + 1);
  await page.getByRole('textbox', { name: 'Request URL', exact: true }).fill('http://127.0.0.1:18080/health');
  await page.locator('#saveReqBtn').click();
});

test('settings booleans and TLS warning match saved values', async ({ page }) => {
  let settings = { timeoutSeconds: 30, followRedirects: true, insecure: false };
  await page.route('**/api/settings', route => {
    if (route.request().method() === 'PUT') settings = route.request().postDataJSON();
    return route.fulfill({ json: settings });
  });
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: 'Follow redirects' })).toBeChecked();
  await expect(page.getByRole('checkbox', { name: 'Skip TLS verification' })).not.toBeChecked();
  await page.getByRole('checkbox', { name: 'Follow redirects' }).uncheck();
  await expect.poll(() => settings.followRedirects).toBe(false);
  await page.getByRole('checkbox', { name: 'Skip TLS verification' }).check();
  await expect.poll(() => settings.insecure).toBe(true);
  await expect(page.locator('#tlsStatus')).toBeVisible();
  await page.getByRole('button', { name: 'Collections', exact: true }).click();
  await expect(page.locator('#tlsStatus')).toBeVisible();
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: 'Follow redirects' })).not.toBeChecked();
  await expect(page.getByRole('checkbox', { name: 'Skip TLS verification' })).toBeChecked();
  await page.getByRole('checkbox', { name: 'Follow redirects' }).check();
  await page.getByRole('checkbox', { name: 'Skip TLS verification' }).uncheck();
  await expect.poll(() => settings.insecure).toBe(false);
  await expect.poll(() => settings.followRedirects).toBe(true);
  await expect(page.locator('#tlsStatus')).toBeHidden();
});

test('interface mode persists and environment changes preserve the active view', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByLabel('Interface mode', { exact: true }).selectOption('basic');
  await page.reload({ waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.getByLabel('Interface mode', { exact: true })).toHaveValue('basic');
  await expect(page.getByRole('button', { name: 'Test management', exact: true })).toBeHidden();
  await page.getByLabel('Interface mode', { exact: true }).selectOption('full');
  for (const [name, title] of [['History', 'History'], ['Settings', 'Settings'], ['Header presets', 'Header Presets'], ['Environments', 'Environments'], ['Test management', 'Test Management']]) {
    const navigation = page.getByRole('button', { name, exact: true });
    await navigation.click();
    if (name === 'Test management') await expect(page.locator('.tmRequestSummary')).toBeVisible();
    else await expect(page.locator('#crumb')).toContainText(title);
    const activeCrumb = await page.locator('#crumb').innerText();
    await page.locator('#envSel').selectOption('local');
    await page.waitForLoadState('networkidle');
    await expect(page.locator('#crumb')).toHaveText(activeCrumb);
    await expect(navigation).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#url')).toHaveCount(0);
    await page.locator('#envSel').selectOption('');
  }
});
