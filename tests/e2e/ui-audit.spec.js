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
  await expect(page.getByRole('table', { name: 'API tests' })).toBeVisible();
  await page.getByRole('table', { name: 'API tests' }).locator('.tmNameCell .tmTextButton').first().click();
  await expect(page.locator('.tmRequestSummary')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Push execution', exact: true })).toHaveCount(0);
  const details = page.getByRole('tab', { name: 'Details', exact: true });
  await details.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByLabel('Test name', { exact: true })).toBeVisible();
  await page.getByRole('tab', { name: 'Assertions', exact: true }).focus();
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

test('sidebar branding and icons keep their proportions', async ({ page }) => {
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  for (const view of ['Collections', 'Test management']) {
    await page.getByRole('button', { name: view, exact: true }).click();
    if (view === 'Test management') await expect(page.getByRole('table', { name: 'API tests' })).toBeVisible();
    for (const viewport of viewports) {
      await page.setViewportSize(viewport);
      for (const expanded of [true, false]) {
        const toggle = page.locator('#railToggle');
        if (await toggle.getAttribute('aria-expanded') !== String(expanded)) await toggle.click();
        await expect(toggle).toHaveAttribute('aria-expanded', String(expanded));
        const rail = await page.locator('#rail').boundingBox();
        const logo = await page.locator('#brand .logo').boundingBox();
        expect(logo.width).toBe(26);
        expect(logo.height).toBe(26);
        expect(logo.x).toBeGreaterThanOrEqual(rail.x);
        expect(logo.x + logo.width).toBeLessThanOrEqual(rail.x + rail.width);
        if (expanded && viewport.width > 760) await expect(page.locator('.brandName b')).toBeVisible();
        const icons = await page.locator('.navIcon:visible').evaluateAll(elements => elements.map(element => {
          const icon = element.getBoundingClientRect();
          const button = element.parentElement.getBoundingClientRect();
          return { width: icon.width, height: icon.height, left: icon.left, right: icon.right,
            center: icon.left + icon.width / 2, buttonCenter: button.left + button.width / 2,
            buttonHeight: button.height };
        }));
        expect(icons).toHaveLength(6);
        for (const icon of icons) {
          expect(icon.width).toBe(20);
          expect(icon.height).toBe(20);
          expect(icon.buttonHeight).toBeGreaterThanOrEqual(40);
          expect(icon.left).toBeGreaterThanOrEqual(rail.x);
          expect(icon.right).toBeLessThanOrEqual(rail.x + rail.width);
          if (!expanded || viewport.width <= 760) expect(Math.abs(icon.center - icon.buttonCenter)).toBeLessThan(1);
        }
        if (view === 'Collections' && ['default', 'minimum'].includes(viewport.name)) {
          await page.screenshot({ path: `${outDir}/sidebar-${viewport.name}-${expanded ? 'expanded' : 'collapsed'}.png` });
        }
      }
    }
  }
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

test('history rows and Inspect reveal response snapshots', async ({ page }) => {
  const entries = Array.from({ length: 20 }, (_, index) => ({
    id: 98765 + index, requestName: `Snapshot ${index + 1}`, method: 'GET',
    url: `https://example.test/${index + 1}`, status: 200, durationMs: 15,
    sentAt: new Date().toISOString(),
  }));
  await page.route('**/api/history?*', route => route.fulfill({ json: entries }));
  await page.route('**/api/history/9876*', route => {
    const id = Number(new URL(route.request().url()).pathname.split('/').pop());
    route.fulfill({ json: { entry: entries.find(entry => entry.id === id), body: `response ${id}` } });
  });
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'History', exact: true }).click();
  const rows = page.locator('.historyView .histRow');
  await expect(rows).toHaveCount(20);
  await rows.first().getByRole('button', { name: 'Inspect' }).click();
  await expect(page.getByRole('heading', { name: 'Response snapshot' })).toBeInViewport();
  await expect(page.locator('.historyView pre')).toContainText('response 98765');
  await rows.nth(1).getByText('Snapshot 2').click();
  await expect(page.locator('.historyView pre')).toContainText('response 98766');
  await expect(page.getByRole('heading', { name: 'Response snapshot' })).toBeInViewport();
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
    if (name === 'Test management') await expect(page.getByRole('table', { name: 'API tests' })).toBeVisible();
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

test('Postman environment import preserves the selected collection across conflicts', async ({ page }) => {
  const name = 'imported-policy-sit';
  const collectionId = 987654;
  const queries = [];
  const downloads = [];
  let conflict = false;
  let imported = false;
  page.on('download', download => downloads.push(download.suggestedFilename()));
  await page.route('**/api/state', async route => {
    const response = await route.fetch();
    const state = await response.json();
    state.storageMode = 'files';
    state.workspaceHash = conflict ? 'workspace-after-conflict' : 'workspace-before-conflict';
    state.collections = [...state.collections.slice(0, 1), { id: collectionId, name: 'Policy API', requests: [], folders: [] }];
    if (imported) state.environments.push({ name, vars: { baseUrl: 'https://policy.example.test' }, secrets: ['apiToken'] });
    await route.fulfill({ json: state });
  });
  await page.route('**/api/workspace/refresh-status', route => route.fulfill({ json: { generation: 0 } }));
  await page.route('**/api/import/postman?**', async route => {
    const query = new URL(route.request().url()).searchParams;
    queries.push(Object.fromEntries(query));
    const result = { kind: 'environment', environmentName: name, variables: 2, secrets: 1, requests: 0, warnings: [], collectionId, workspaceHash: conflict ? 'workspace-after-conflict' : 'workspace-before-conflict', contentHash: conflict ? 'environment-after-conflict' : '' };
    if (query.get('preview') === '1') {
      await route.fulfill({ json: result });
    } else if (!conflict) {
      conflict = true;
      await route.fulfill({ status: 409, json: { error: 'Environment changed on disk' } });
    } else {
      imported = true;
      await route.fulfill({ json: result });
    }
  });
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Environments', exact: true }).click();
  await page.getByRole('button', { name: 'Import', exact: true }).click();
  await expect(page.getByLabel('Import format', { exact: true })).toContainText('Postman collection or environment JSON');
  await page.getByLabel('Environment import collection', { exact: true }).selectOption(String(collectionId));
  const chooser = page.waitForEvent('filechooser');
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await (await chooser).setFiles({ name: 'policy.postman_environment.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify({ name: 'Imported Policy SIT', _postman_variable_scope: 'environment', values: [{ key: 'baseUrl', value: 'https://policy.example.test', enabled: true }, { key: 'apiToken', value: 'synthetic-secret', type: 'secret', enabled: true }] })) });
  await expect(page.locator('#modalMsg')).toContainText('2 variables (1 secret)');
  await page.getByRole('button', { name: 'Import environment', exact: true }).click();
  await expect(page.locator('#modalMsg')).toContainText('An existing environment with this name will be replaced.');
  await expect(page.locator('#modalMsg')).toContainText('The workspace changed before commit.');
  await page.screenshot({ path: `${outDir}/postman-environment-preview.png`, fullPage: true });
  await page.getByRole('button', { name: 'Import environment', exact: true }).click();
  await expect(page.locator('#envSel')).toHaveValue(name);
  await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
  await expect(page.locator('#work')).toContainText('RELAY_SECRET_APITOKEN');
  await expect(page.locator('#work')).not.toContainText('synthetic-secret');
  expect(queries).toEqual([
    { collectionId: String(collectionId), preview: '1' },
    { collectionId: String(collectionId), workspaceHash: 'workspace-before-conflict', contentHash: '' },
    { collectionId: String(collectionId), preview: '1' },
    { collectionId: String(collectionId), workspaceHash: 'workspace-after-conflict', contentHash: 'environment-after-conflict' },
  ]);
  expect(downloads).toEqual([]);
  await audit(page, 'postman-environment-import');
});

async function seedTeamTests(page) {
  const post = async (path, data) => {
    const response = await page.request.post(new URL(path, baseURL).href, { data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  const collection = await post('/api/collections', { name: 'Team API ' + Date.now() });
  const tests = [];
  for (const [name, status, enabled] of [['Account health', 200, true], ['Reject invalid account', 422, true], ['Paused contract', 200, false]]) {
    const request = await post('/api/requests', { collectionId: collection.id, spec: { name, method: 'GET', url: 'http://127.0.0.1:18080/health' } });
    const managed = await post('/api/tests', { requestId: request.id, name, enabled: true, owner: 'Payments QA', priority: 'high', tags: ['smoke'], assertions: [{ type: 'status', equals: status }], xrayKey: name === 'Account health' ? 'QA-42' : '' });
    if (!enabled) {
      const response = await page.request.put(new URL('/api/tests/' + managed.id, baseURL).href, { data: { ...managed, enabled: false } });
      expect(response.ok()).toBeTruthy();
    }
    tests.push(managed);
  }
  await page.goto(baseURL, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Test management', exact: true }).click();
  await page.getByLabel('Filter by collection', { exact: true }).selectOption(String(collection.id));
  return { collection, tests };
}

test('team test inventory scopes runs and retains execution failures', async ({ page }) => {
  const { tests } = await seedTeamTests(page);
  await page.getByRole('button', { name: 'Actions for Account health', exact: true }).click();
  await expect(page.locator('#ctxMenu')).toHaveClass(/open/);
  await expect(page.locator('#ctxMenu')).toContainText('Open');
  await page.keyboard.press('Escape');
  await page.getByRole('checkbox', { name: 'Select all visible tests' }).check();
  await expect(page.getByRole('button', { name: 'Run selected (2)', exact: true })).toBeEnabled();
  await page.getByRole('searchbox', { name: 'Search tests', exact: true }).fill('no matching team');
  await expect(page.getByRole('button', { name: 'Run selected (0)', exact: true })).toBeDisabled();
  await expect(page.getByRole('heading', { name: 'No matching tests' })).toBeVisible();
  await page.getByRole('searchbox', { name: 'Search tests', exact: true }).fill('payments');
  await expect(page.getByRole('table', { name: 'API tests' }).locator('tbody tr')).toHaveCount(3);
  await page.getByRole('checkbox', { name: 'Select all visible tests' }).check();
  await page.getByRole('button', { name: 'Run selected (2)', exact: true }).click();
  await expect(page.getByRole('heading', { name: '1/2 passed', exact: true })).toBeVisible();
  await expect(page.locator('#tmExecutionResults .tmResult')).toHaveCount(2);
  await expect(page.locator('#tmExecutionResults .tmResult[open]')).toContainText('Reject invalid account');
  await expect(page.locator('#tmExecutionResults')).toContainText('HTTP 200');
  await expect(page.locator('#tmExecutionResults')).toContainText('Expected:');
  await expect(page.locator('#tmExecutionResults')).toContainText('expected status 422, got 200');
  await page.getByRole('checkbox', { name: 'Show failed results only' }).check();
  await expect(page.locator('#tmExecutionResults .tmResult')).toHaveCount(1);
  const executionName = await page.locator('.tmPageHead h1').innerText();
  await page.getByRole('button', { name: 'All executions', exact: true }).click();
  await page.getByRole('button', { name: executionName, exact: true }).click();
  await expect(page.getByRole('heading', { name: '1/2 passed', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Rerun failed (1)', exact: true }).click();
  await expect(page.getByRole('heading', { name: '0/1 passed', exact: true })).toBeVisible();
  const state = await (await page.request.get(new URL('/api/tests/state', baseURL).href)).json();
  expect(state.lastRuns[tests[2].id]).toBeUndefined();
  await audit(page, 'tm-execution-failure');
  await page.reload({ waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Test management', exact: true }).click();
  await page.getByRole('navigation', { name: 'Test management views' }).getByRole('button', { name: /^Executions/ }).click();
  await page.getByRole('button', { name: executionName, exact: true }).click();
  await expect(page.getByRole('heading', { name: '1/2 passed', exact: true })).toBeVisible();
});

test('managed edits flush before running and Xray links remain available', async ({ page }) => {
  const { tests } = await seedTeamTests(page);
  await page.getByRole('table', { name: 'API tests' }).getByRole('button', { name: 'Account health', exact: true }).click();
  await page.getByRole('tab', { name: 'Xray links', exact: true }).click();
  await expect(page.getByLabel('Xray test key', { exact: true })).toHaveValue('QA-42');
  let validatedKey;
  await page.route('**/api/xray/tests/*/validate', async route => {
    const saved = await (await page.request.get(new URL('/api/tests/' + tests[0].id, baseURL).href)).json();
    validatedKey = saved.xrayKey;
    await route.fulfill({ json: { key: saved.xrayKey } });
  });
  await page.getByLabel('Xray test key', { exact: true }).fill('QA-43');
  await page.getByRole('button', { name: 'Validate link', exact: true }).click();
  await expect.poll(() => validatedKey).toBe('QA-43');
  await page.getByRole('tab', { name: 'Assertions', exact: true }).click();
  await page.getByLabel('Test script', { exact: true }).fill('pm.test("fresh edit", function () { pm.expect(200).to.equal(201); });');
  await page.getByRole('button', { name: 'Run test', exact: true }).click();
  await expect(page.getByRole('heading', { name: '0/1 passed', exact: true })).toBeVisible();
  await expect(page.locator('#tmExecutionResults')).toContainText('fresh edit');
  await page.getByRole('button', { name: 'Edit test', exact: true }).click();
  await page.getByRole('tab', { name: 'Details', exact: true }).click();
  let executionRequests = 0;
  page.on('request', request => { if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/test-executions') executionRequests++; });
  await page.route('**/api/tests/' + tests[0].id, route => route.request().method() === 'PUT'
    ? route.fulfill({ status: 500, json: { error: 'Save unavailable' } }) : route.continue());
  await page.getByRole('textbox', { name: 'Test name', exact: true }).fill('Unsaved team edit');
  await page.getByRole('button', { name: 'Run test', exact: true }).click();
  await expect(page.locator('#tmSaveState')).toContainText('Not saved');
  await expect(page.getByRole('textbox', { name: 'Test name', exact: true })).toHaveValue('Unsaved team edit');
  expect(executionRequests).toBe(0);
  await page.unroute('**/api/tests/' + tests[0].id);
  await page.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(page.locator('#tmSaveState')).toHaveText('All changes saved');
});

test('test management inventory and editor fit desktop and mobile', async ({ page }) => {
  await seedTeamTests(page);
  for (const viewport of [...viewports, { name: 'mobile', width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await audit(page, 'tm-inventory-' + viewport.name);
  }
  await page.getByRole('table', { name: 'API tests' }).getByRole('button', { name: 'Account health', exact: true }).click();
  for (const name of ['Assertions', 'Request', 'Details', 'Last result', 'Xray links']) {
    await page.getByRole('tab', { name, exact: true }).click();
    await audit(page, 'tm-mobile-' + name.toLowerCase().replaceAll(' ', '-'));
  }
});

test('teams organize tests into folders and sets without changing assertions', async ({ page }) => {
  const { tests } = await seedTeamTests(page);
  const folderName = 'Account contracts ' + Date.now();
  await page.getByRole('button', { name: '+ New folder', exact: true }).click();
  await page.locator('#modalInput').fill(folderName);
  await page.locator('#modalOk').click();
  await expect(page.locator('#sideList')).toContainText(folderName);
  await page.getByRole('checkbox', { name: 'Select Reject invalid account', exact: true }).check();
  await page.getByRole('button', { name: 'Organize', exact: true }).click();
  await page.getByRole('button', { name: 'Move to folder', exact: true }).click();
  await page.locator('#modalOk').click();
  await page.getByRole('button', { name: folderName, exact: true }).click();
  await page.locator('#modalOk').click();
  await expect(page.getByRole('button', { name: 'Run selected (0)', exact: true })).toBeDisabled();
  const moved = await (await page.request.get(new URL('/api/tests/' + tests[1].id, baseURL).href)).json();
  expect(moved.folderId).toBeTruthy();
  expect(moved.assertions[0].equals).toBe(422);
  await page.getByRole('checkbox', { name: 'Select Account health', exact: true }).check();
  await page.getByRole('checkbox', { name: 'Select Reject invalid account', exact: true }).check();
  await page.getByRole('button', { name: 'Organize', exact: true }).click();
  await page.getByRole('button', { name: 'Add to test set', exact: true }).click();
  await page.locator('#modalOk').click();
  await page.getByRole('button', { name: 'New test set', exact: true }).click();
  await page.locator('#modalOk').click();
  const setName = 'Team smoke ' + Date.now();
  await page.locator('#modalInput').fill(setName);
  await page.locator('#modalOk').click();
  await expect(page.getByRole('button', { name: 'Run selected (0)', exact: true })).toBeDisabled();
  await page.getByRole('navigation', { name: 'Test management views' }).getByRole('button', { name: /^Test sets/ }).click();
  await page.getByRole('button', { name: setName, exact: true }).click();
  await page.getByRole('button', { name: 'Run set', exact: true }).click();
  await expect(page.getByRole('heading', { name: '1/2 passed', exact: true })).toBeVisible();
  await page.getByRole('navigation', { name: 'Test management views' }).getByRole('button', { name: /^Tests\b/ }).click();
  await page.getByRole('checkbox', { name: 'Select Account health', exact: true }).check();
  let exported;
  await page.route('**/api/tests/export', route => {
    exported = route.request().postDataJSON();
    return route.fulfill({ contentType: 'application/zip', body: Buffer.from('test export') });
  });
  await page.getByRole('button', { name: 'Export', exact: true }).click();
  await page.getByRole('button', { name: '1 selected tests', exact: true }).click();
  await page.locator('#modalOk').click();
  await page.getByRole('button', { name: 'Relay pack', exact: true }).click();
  await page.locator('#modalOk').click();
  await expect.poll(() => exported?.testIds).toEqual([tests[0].id]);
  expect(exported.scope).toBe('tests');
});
