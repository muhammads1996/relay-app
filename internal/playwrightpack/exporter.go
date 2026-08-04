// Package playwrightpack generates a TypeScript Playwright API test project
// from Relay Test Management state.
package playwrightpack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/muhaymien96/relay/internal/dsl"
	"github.com/muhaymien96/relay/internal/store"
	"github.com/muhaymien96/relay/internal/vars"
)

type Project struct {
	Name               string
	DefaultEnvironment string
	Cases              []Case
	Envs               []store.Environment
}

type Case struct {
	ID                string
	Test              store.TestCase
	Request           dsl.Request
	CollectionName    string
	FolderName        string
	CollectionVars    map[string]string
	FolderVars        map[string]string
	CollectionHeaders map[string]string
	FolderHeaders     map[string]string
}

func Export(root string, p Project) error {
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "Relay Playwright Suite"
	}
	if len(p.Cases) == 0 {
		return fmt.Errorf("no tests selected for Playwright export")
	}
	for _, dir := range []string{"tests/models", "tests/services", "tests/fixtures", "tests/specs", "tests/helpers", "reporters", "scripts"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return err
		}
	}
	files := map[string]string{
		"package.json":                              packageJSON(p.Name),
		"playwright.config.ts":                      playwrightConfig(),
		"tsconfig.json":                             tsconfig(),
		".gitignore":                                generatedGitignore(),
		".env.example":                              envExample(p),
		".env":                                      envFile(p),
		"README.md":                                 readme(p.Name, p.DefaultEnvironment),
		"tests/models/relay.model.ts":               relayModels(),
		"tests/models/request-models.model.ts":      requestModels(p.Cases),
		"tests/fixtures/index.ts":                   relayFixture(),
		"tests/fixtures/test-data.ts":               testData(p),
		"tests/fixtures/test-metadata.generated.ts": generatedMetadata(p.Cases),
		"tests/helpers/assertions.ts":               assertionHelpers(),
		"tests/services/api.service.ts":             apiService(),
		"reporters/relay-xray-reporter.ts":          relayReporter(),
		"scripts/import-xray.mjs":                   importXrayScript(),
		"tests/specs/relay-generated-suite.spec.ts": generatedTests(p.Cases),
	}
	for _, svc := range serviceFiles(p.Cases) {
		files[svc.path] = svc.body
	}
	for path, body := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func packageJSON(name string) string {
	doc := map[string]any{
		"name":        slug(name),
		"private":     true,
		"description": "Generated Relay Playwright API test suite",
		"scripts": map[string]string{
			"test":        "playwright test",
			"test:list":   "playwright test --list",
			"typecheck":   "tsc --noEmit",
			"xray:import": "node scripts/import-xray.mjs",
		},
		"devDependencies": map[string]string{
			"@playwright/test": "^1.45.0",
			"@types/node":      "^22.0.0",
			"dotenv":           "^16.4.5",
			"typescript":       "^5.5.0",
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b) + "\n"
}

func generatedGitignore() string {
	return ".env\nnode_modules/\nplaywright-report/\ntest-results/\nrelay-playwright-results.json\n"
}

func playwrightConfig() string {
	return `import { defineConfig } from '@playwright/test';
import dotenv from 'dotenv';

dotenv.config();

export default defineConfig({
  testDir: './tests/specs',
  reporter: [
    ['list'],
    ['./reporters/relay-xray-reporter.ts'],
  ],
  use: {
    extraHTTPHeaders: {},
  },
});
`
}

func tsconfig() string {
	return `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "commonjs",
    "moduleResolution": "node",
    "strict": true,
    "esModuleInterop": true,
    "resolveJsonModule": true,
    "types": ["node", "@playwright/test"]
  },
  "include": ["tests/**/*.ts", "reporters/**/*.ts"]
}
`
}

func envExample(p Project) string {
	keys := map[string]bool{"Environment": true, "XRAY_PROJECT": true, "XRAY_TEST_PLAN": true, "XRAY_SUMMARY": true}
	for _, e := range p.Envs {
		for k := range e.Vars {
			keys["RELAY_VAR_"+envKey(k)] = true
		}
		for _, s := range e.Secrets {
			keys[vars.SecretEnvVar(s)] = true
		}
	}
	var out []string
	for k := range keys {
		out = append(out, k+"=")
	}
	sort.Strings(out)
	return strings.Join(out, "\n") + "\n"
}

func envFile(p Project) string {
	lines := []string{"Environment=" + p.DefaultEnvironment}
	if strings.TrimSpace(p.DefaultEnvironment) == "" {
		lines[0] = "Environment="
	}
	for _, e := range p.Envs {
		if e.Name != p.DefaultEnvironment {
			continue
		}
		for _, k := range sortedStringKeys(e.Vars) {
			lines = append(lines, "RELAY_VAR_"+envKey(k)+"="+e.Vars[k])
		}
		for _, s := range sortedStrings(e.Secrets) {
			lines = append(lines, vars.SecretEnvVar(s)+"=")
		}
	}
	lines = append(lines, "XRAY_PROJECT=", "XRAY_TEST_PLAN=", "XRAY_SUMMARY=")
	return strings.Join(lines, "\n") + "\n"
}

func importXrayScript() string {
	return `import 'dotenv/config';
import { spawnSync } from 'node:child_process';

const project = process.env.XRAY_PROJECT?.trim();
if (!project) {
  console.error('Set XRAY_PROJECT in .env or the process environment before importing.');
  process.exit(2);
}

const args = ['xray', 'import', 'relay-playwright-results.json', '--project', project];
if (process.env.XRAY_TEST_PLAN?.trim()) args.push('--plan', process.env.XRAY_TEST_PLAN.trim());
if (process.env.XRAY_SUMMARY?.trim()) args.push('--summary', process.env.XRAY_SUMMARY.trim());

const result = spawnSync('relay', args, { stdio: 'inherit', shell: process.platform === 'win32' });
process.exit(result.status ?? 1);
`
}

func readme(name, defaultEnv string) string {
	return strings.Join([]string{
		"# " + name,
		"",
		"Generated by Relay Test Management.",
		"",
		"## Environment",
		"",
		"The project loads `.env` through Playwright config. Set `Environment` to switch between exported Relay environments:",
		"",
		"```bash",
		"Environment=" + defaultEnv + " npx playwright test",
		"```",
		"",
		"Request variables are resolved from Relay request/folder/collection data first, then the selected environment in `tests/fixtures/test-data.ts`, then `RELAY_VAR_*` and `RELAY_SECRET_*` process variables.",
		"",
		"## Run",
		"",
		"```bash",
		"npm install",
		"npm run typecheck",
		"npx playwright test",
		"npm run xray:import",
		"```",
		"",
		"The generated reporter writes `relay-playwright-results.json` for Relay/Xray import. Secret values are never exported; fill the empty `RELAY_SECRET_*` entries locally or inject them in CI.",
		"",
	}, "\n")
}

func relayModels() string {
	return `export type RelayStatus = 'PASSED' | 'FAILED' | 'SKIPPED';

export interface RelayXrayMetadata {
  testKey?: string;
  testPlanKey?: string;
  requirements?: string[];
}

export interface RelayTestMetadata {
  id: string;
  name: string;
  owner?: string;
  priority?: string;
  tags?: string[];
  xray?: RelayXrayMetadata;
}

export interface RelayStepResult {
  name: string;
  type: string;
  expected?: string;
  actual?: string;
  status: RelayStatus;
  comment?: string;
}

export interface RelayTestResult {
  id: string;
  name: string;
  testKey?: string;
  testPlanKey?: string;
  requirements?: string[];
  status: RelayStatus;
  durationMs: number;
  comment?: string;
  steps: RelayStepResult[];
}

export interface RelayResultsFile {
  schema: 'relay.playwright.results.v1';
  startedAt: string;
  finishedAt: string;
  summary: {
    tests: number;
    passed: number;
    failed: number;
    skipped: number;
  };
  results: RelayTestResult[];
}
`
}

func requestModels(cases []Case) string {
	var b strings.Builder
	b.WriteString("// Inferred from JSON request bodies at export time.\n\n")
	wrote := false
	for _, c := range cases {
		if c.Request.Body == nil || strings.ToLower(c.Request.Body.Type) != "json" {
			continue
		}
		var doc any
		if json.Unmarshal([]byte(c.Request.Body.Content), &doc) != nil {
			continue
		}
		obj, ok := doc.(map[string]any)
		if !ok {
			continue
		}
		wrote = true
		fmt.Fprintf(&b, "export interface %sRequest {\n", pascal(c.Test.Name))
		for _, key := range sortedAnyKeys(obj) {
			fmt.Fprintf(&b, "  %s: %s;\n", tsProp(key), tsType(obj[key]))
		}
		b.WriteString("}\n\n")
	}
	if !wrote {
		b.WriteString("export type RelayGeneratedRequestBody = Record<string, unknown>;\n")
	}
	return b.String()
}

func relayFixture() string {
	return `import { test as base, expect, type TestInfo } from '@playwright/test';

export { expect };
export const test = base.extend({});

export async function readResponseBody(response: { text(): Promise<string> }): Promise<string> {
  return response.text();
}

export async function attachRequestResponse(
  testInfo: TestInfo,
  status: number,
  bodyText: string,
  requestBody?: string,
) {
  await testInfo.attach('relay-request-response.json', {
    contentType: 'application/json',
    body: Buffer.from(JSON.stringify({
      status,
      requestBody: requestBody || undefined,
      responseBody: tryParseJSON(bodyText),
    }, null, 2)),
  });
}

export async function relayStep(
  testInfo: TestInfo,
  name: string,
  type: string,
  expected: string | undefined,
  body: () => Promise<unknown>,
) {
  await test.step(name, async () => {
    try {
      await body();
      testInfo.annotations.push({
        type: 'relay-step',
        description: JSON.stringify({ name, type, expected, status: 'PASSED' }),
      });
    } catch (err) {
      testInfo.annotations.push({
        type: 'relay-step',
        description: JSON.stringify({
          name,
          type,
          expected,
          status: 'FAILED',
          comment: err instanceof Error ? err.message : String(err),
        }),
      });
      throw err;
    }
  });
}

function tryParseJSON(input: string): unknown {
  if (!input) return '';
  try { return JSON.parse(input); } catch { return input; }
}
`
}

func testData(p Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "export const defaultEnvironment = %s;\n", js(p.DefaultEnvironment))
	b.WriteString("export const Environment = process.env.Environment || defaultEnvironment;\n\n")
	b.WriteString("export const relayEnvironments = {\n")
	for _, e := range p.Envs {
		fmt.Fprintf(&b, "  %s: {\n", js(e.Name))
		fmt.Fprintf(&b, "    vars: {\n")
		for _, k := range sortedStringKeys(e.Vars) {
			fmt.Fprintf(&b, "      %s: %s,\n", js(k), js(e.Vars[k]))
		}
		fmt.Fprintf(&b, "    },\n")
		fmt.Fprintf(&b, "    secrets: %s,\n", jsStringArray(e.Secrets))
		fmt.Fprintf(&b, "  },\n")
	}
	b.WriteString("} as const;\n\n")
	b.WriteString(`export function environmentVars(): Record<string, string> {
  const selected = relayEnvironments[Environment as keyof typeof relayEnvironments];
  const vars: Record<string, string> = {};
  if (selected?.vars) Object.assign(vars, selected.vars);
  for (const [key, value] of Object.entries(process.env)) {
    if (key.startsWith('RELAY_VAR_') && value !== undefined) {
      vars[fromRelayEnvKey(key.slice('RELAY_VAR_'.length))] = value;
    }
  }
  for (const secretName of selected?.secrets || []) {
    const value = process.env[secretEnvVar(secretName)];
    if (value !== undefined) vars[secretName] = value;
  }
  return vars;
}

export function secretEnvVar(name: string): string {
  return 'RELAY_SECRET_' + name.replace(/[^a-zA-Z0-9]/g, '_').toUpperCase();
}

function fromRelayEnvKey(name: string): string {
  return name.toLowerCase().replace(/_([a-z0-9])/g, (_match, ch) => String(ch).toUpperCase());
}
`)
	return b.String()
}

func assertionHelpers() string {
	return `import { expect } from '../fixtures';

export function assertStatus(actual: number, expected: number) {
  expect(actual).toBe(expected);
}

export function assertApiError(body: unknown, label: string) {
  expect(body, label).toBeDefined();
}
`
}

func apiService() string {
	return `import { randomUUID } from 'crypto';
import type { APIRequestContext, TestInfo } from '@playwright/test';
import type { RelayTestMetadata } from '../models/relay.model';
import { environmentVars, secretEnvVar } from '../fixtures/test-data';

export interface RelayRequestInput {
  method: string;
  url: string;
  query?: Record<string, string>;
  headers?: Record<string, string>;
  body?: string;
  bodyType?: string;
  auth?: {
    type?: string;
    token?: string;
    username?: string;
    password?: string;
    key?: string;
    value?: string;
    in?: string;
  } | null;
  scripts?: {
    preRequest?: string;
    tests?: string;
  };
  vars?: Record<string, string>;
  metadata: RelayTestMetadata;
}

export interface RelayAPIResult {
  response: Awaited<ReturnType<APIRequestContext['fetch']>>;
  durationMs: number;
}

export interface RelayScriptTestResult {
  name: string;
  passed: boolean;
  error?: string;
}

export class SessionVars {
  private values = new Map<string, string>();

  get(name: string): string | undefined {
    return this.values.get(name);
  }

  set(name: string, value: string) {
    this.values.set(name, value);
  }

  all(): Record<string, string> {
    return Object.fromEntries(this.values.entries());
  }
}

export class ApiService {
  constructor(private request: APIRequestContext, private session: SessionVars) {}

  async fetch(input: RelayRequestInput, testInfo: TestInfo): Promise<RelayAPIResult> {
    testInfo.annotations.push({ type: 'relay-test', description: JSON.stringify(input.metadata) });
    runPreRequestScript(input.scripts?.preRequest, this.session, {
      ...environmentVars(),
      ...this.session.all(),
      ...(input.vars || {}),
    });
    const vars = { ...environmentVars(), ...this.session.all(), ...(input.vars || {}) };
    let url = interpolate(input.url, vars);
    if (input.query) {
      const parsed = new URL(url);
      for (const [key, value] of Object.entries(input.query)) {
        parsed.searchParams.set(key, interpolate(value, vars));
      }
      url = parsed.toString();
    }
    const headers: Record<string, string> = {};
    for (const [key, value] of Object.entries(input.headers || {})) {
      if (value !== '') headers[key] = interpolate(value, vars);
    }
    url = applyAuth(input, url, headers, vars);
    const options: Parameters<APIRequestContext['fetch']>[1] = {
      method: input.method,
      headers,
    };
    if (input.body !== undefined && input.body !== '') {
      options.data = interpolate(input.body, vars);
    }
    const started = Date.now();
    const response = await this.request.fetch(url, options);
    return { response, durationMs: Date.now() - started };
  }
}

function applyAuth(input: RelayRequestInput, url: string, headers: Record<string, string>, vars: Record<string, string>): string {
  const auth = input.auth;
  if (!auth || !auth.type || auth.type === 'none') return url;
  if (auth.type === 'bearer' && auth.token) {
    headers.Authorization = 'Bearer ' + interpolate(auth.token, vars);
  } else if (auth.type === 'basic') {
    const raw = interpolate(auth.username || '', vars) + ':' + interpolate(auth.password || '', vars);
    headers.Authorization = 'Basic ' + Buffer.from(raw).toString('base64');
  } else if (auth.type === 'apikey' && auth.key && auth.value) {
    if (auth.in === 'query') {
      const parsed = new URL(url);
      parsed.searchParams.set(auth.key, interpolate(auth.value, vars));
      return parsed.toString();
    }
    headers[auth.key] = interpolate(auth.value, vars);
  }
  return url;
}

export function interpolate(input: string, vars: Record<string, string>): string {
  return input.replace(/\{\{\s*([^{}]+?)\s*\}\}/g, (_match, raw) => {
    const name = String(raw).trim();
    if (name === '$uuid') return randomUUID();
    if (name === '$timestamp') return String(Math.floor(Date.now() / 1000));
    if (name === '$isoTimestamp') return new Date().toISOString();
    if (name === '$randomInt') return String(Math.floor(Math.random() * 1000));
    if (vars[name] !== undefined) return vars[name];
    const envName = 'RELAY_VAR_' + name.replace(/[^a-zA-Z0-9]/g, '_').toUpperCase();
    if (process.env[envName] !== undefined) return process.env[envName]!;
    const secretName = secretEnvVar(name);
    if (process.env[secretName] !== undefined) return process.env[secretName]!;
    throw new Error('Unresolved Relay variable {{' + name + '}}');
  });
}

export function parseJSONBody(bodyText: string): unknown {
  if (!bodyText) return undefined;
  return JSON.parse(bodyText);
}

export function makePM(response: {
  bodyText: string;
  status: number;
  statusText: string;
  durationMs: number;
  headers: Record<string, string>;
}, session: SessionVars, vars: Record<string, string> = {}) {
  return {
    test: (_name: string, fn: () => unknown) => fn(),
    expect: (value: unknown) => ({
      to: {
        equal: (want: unknown) => {
          if (JSON.stringify(value) !== JSON.stringify(want)) {
            throw new Error('expected ' + JSON.stringify(value) + ' to equal ' + JSON.stringify(want));
          }
        },
        eql: (want: unknown) => {
          if (JSON.stringify(value) !== JSON.stringify(want)) {
            throw new Error('expected ' + JSON.stringify(value) + ' to eql ' + JSON.stringify(want));
          }
        },
        include: (want: string) => {
          if (!String(value).includes(want)) throw new Error('expected ' + value + ' to include ' + want);
        },
        be: {
          below: (want: number) => {
            if (Number(value) >= want) throw new Error('expected ' + value + ' to be below ' + want);
          },
          above: (want: number) => {
            if (Number(value) <= want) throw new Error('expected ' + value + ' to be above ' + want);
          },
        },
      },
    }),
    response: {
      code: response.status,
      status: response.statusText,
      responseTime: response.durationMs,
      json: () => parseJSONBody(response.bodyText),
      text: () => response.bodyText,
      headers: {
        get: (key: string) => response.headers[key.toLowerCase()],
      },
      to: {
        have: {
          status: (want: number) => {
            if (response.status !== want) throw new Error('expected status ' + want + ' but got ' + response.status);
          },
        },
      },
    },
    collectionVariables: {
      set: (key: string, value: unknown) => session.set(key, String(value)),
      get: (key: string) => session.get(key),
    },
    environment: {
      set: (key: string, value: unknown) => session.set(key, String(value)),
      get: (key: string) => session.get(key),
    },
    variables: {
      get: (key: string) => session.get(key) ?? vars[key],
    },
  };
}

export function runPreRequestScript(source: string | undefined, session: SessionVars, vars: Record<string, string>) {
  if (!source || !source.trim()) return;
  const response = { bodyText: '', status: 0, statusText: '', durationMs: 0, headers: {} };
  const basePM = makePM(response, session, vars);
  const pm = {
    ...basePM,
    variables: {
      get: (key: string) => session.get(key) ?? vars[key],
    },
  };
  new Function('pm', source)(pm);
}

export function runPostmanTests(source: string | undefined, response: {
  bodyText: string;
  status: number;
  statusText: string;
  durationMs: number;
  headers: Record<string, string>;
}, session: SessionVars, vars: Record<string, string> = {}): RelayScriptTestResult[] {
  if (!source || !source.trim()) return [];
  const results: RelayScriptTestResult[] = [];
  const basePM = makePM(response, session, vars);
  const pm = {
    ...basePM,
    test: (name: string, fn: () => unknown) => {
      const result: RelayScriptTestResult = { name: name || 'Script assertion', passed: false };
      try {
        fn();
        result.passed = true;
      } catch (err) {
        result.error = err instanceof Error ? err.message : String(err);
      }
      results.push(result);
    },
  };
  try {
    new Function('pm', source)(pm);
  } catch (err) {
    results.push({
      name: 'Script runtime',
      passed: false,
      error: err instanceof Error ? err.message : String(err),
    });
  }
  if (results.length === 0) {
    results.push({ name: 'Script assertion', passed: true });
  }
  return results;
}
`
}

type svcFile struct {
	path string
	body string
}

func serviceFiles(cases []Case) []svcFile {
	groups := map[string]string{}
	for _, c := range cases {
		name := c.CollectionName
		if name == "" {
			name = "Relay"
		}
		groups[slug(name)] = pascal(name)
	}
	var files []svcFile
	for slugName, className := range groups {
		files = append(files, svcFile{
			path: "tests/services/" + slugName + ".service.ts",
			body: fmt.Sprintf(`import { ApiService } from './api.service';

export class %sService extends ApiService {}
`, className),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files
}

func relayReporter() string {
	return `import fs from 'fs';
import type { FullConfig, Reporter, Suite, TestCase, TestResult } from '@playwright/test/reporter';
import type { RelayResultsFile, RelayStepResult, RelayTestMetadata, RelayTestResult } from '../tests/models/relay.model';

class RelayXrayReporter implements Reporter {
  private startedAt = new Date();
  private results: RelayTestResult[] = [];

  onBegin(_config: FullConfig, _suite: Suite) {
    this.startedAt = new Date();
  }

  onTestEnd(test: TestCase, result: TestResult) {
    const metadata = findMetadata(test, result);
    const steps = findSteps(test, result);
    const status = result.status === 'skipped' ? 'SKIPPED' : result.status === 'passed' ? 'PASSED' : 'FAILED';
    this.results.push({
      id: metadata?.id || test.id,
      name: metadata?.name || test.title,
      testKey: metadata?.xray?.testKey,
      testPlanKey: metadata?.xray?.testPlanKey,
      requirements: metadata?.xray?.requirements,
      status,
      durationMs: result.duration,
      comment: result.errors.map(e => e.message || String(e)).join('\n') || undefined,
      steps,
    });
  }

  async onEnd() {
    const summary = {
      tests: this.results.length,
      passed: this.results.filter(r => r.status === 'PASSED').length,
      failed: this.results.filter(r => r.status === 'FAILED').length,
      skipped: this.results.filter(r => r.status === 'SKIPPED').length,
    };
    const out: RelayResultsFile = {
      schema: 'relay.playwright.results.v1',
      startedAt: this.startedAt.toISOString(),
      finishedAt: new Date().toISOString(),
      summary,
      results: this.results,
    };
    fs.writeFileSync('relay-playwright-results.json', JSON.stringify(out, null, 2) + '\n');
  }
}

function annotations(test: TestCase, result: TestResult) {
  const runtime = ((result as unknown as { annotations?: TestCase['annotations'] }).annotations || []);
  return [...runtime, ...test.annotations];
}

function findMetadata(test: TestCase, result: TestResult): RelayTestMetadata | undefined {
  const ann = annotations(test, result).find(a => a.type === 'relay-test' && a.description);
  if (!ann?.description) return undefined;
  try { return JSON.parse(ann.description); } catch { return undefined; }
}

function findSteps(test: TestCase, result: TestResult): RelayStepResult[] {
  return annotations(test, result)
    .filter(a => a.type === 'relay-step' && a.description)
    .map(a => {
      try { return JSON.parse(a.description || '{}') as RelayStepResult; }
      catch { return undefined; }
    })
    .filter((v): v is RelayStepResult => !!v);
}

export default RelayXrayReporter;
`
}

func generatedMetadata(cases []Case) string {
	var b strings.Builder
	b.WriteString("import { SessionVars } from '../services/api.service';\n")
	b.WriteString("import type { RelayRequestInput } from '../services/api.service';\n\n")
	b.WriteString("export const relayRequests: RelayRequestInput[] = [\n")
	for _, c := range cases {
		req := c.Request
		varsMap := mergeMaps(c.CollectionVars, c.FolderVars, req.Vars)
		headers := mergeMaps(c.CollectionHeaders, c.FolderHeaders, req.Headers)
		fmt.Fprintf(&b, "  {\n")
		fmt.Fprintf(&b, "    method: %s,\n", js(req.Method))
		fmt.Fprintf(&b, "    url: %s,\n", js(req.URL))
		writeStringMapTS(&b, "query", req.Query, 4)
		writeStringMapTS(&b, "headers", headers, 4)
		if req.Body != nil {
			fmt.Fprintf(&b, "    body: %s,\n", js(req.Body.Content))
			fmt.Fprintf(&b, "    bodyType: %s,\n", js(req.Body.Type))
		}
		writeAuthTS(&b, req.Auth)
		writeScriptsTS(&b, req.Scripts)
		writeStringMapTS(&b, "vars", varsMap, 4)
		fmt.Fprintf(&b, "    metadata: {\n")
		fmt.Fprintf(&b, "      id: %s,\n", js(c.ID))
		fmt.Fprintf(&b, "      name: %s,\n", js(c.Test.Name))
		if c.Test.Owner != "" {
			fmt.Fprintf(&b, "      owner: %s,\n", js(c.Test.Owner))
		}
		if c.Test.Priority != "" {
			fmt.Fprintf(&b, "      priority: %s,\n", js(c.Test.Priority))
		}
		writeStringSliceTS(&b, "tags", c.Test.Tags, 6)
		fmt.Fprintf(&b, "      xray: {\n")
		if c.Test.XrayKey != "" {
			fmt.Fprintf(&b, "        testKey: %s,\n", js(c.Test.XrayKey))
		}
		if c.Test.TestPlanKey != "" {
			fmt.Fprintf(&b, "        testPlanKey: %s,\n", js(c.Test.TestPlanKey))
		}
		writeStringSliceTS(&b, "requirements", c.Test.Requirements, 8)
		fmt.Fprintf(&b, "      },\n")
		fmt.Fprintf(&b, "    },\n")
		fmt.Fprintf(&b, "  },\n")
	}
	b.WriteString("];\n\n")
	b.WriteString("export const relaySession = new SessionVars();\n")
	return b.String()
}

func generatedTests(cases []Case) string {
	var b strings.Builder
	b.WriteString("import { test, expect, relayStep, readResponseBody, attachRequestResponse } from '../fixtures';\n")
	b.WriteString("import { ApiService, parseJSONBody, runPostmanTests } from '../services/api.service';\n")
	b.WriteString("import { relayRequests, relaySession } from '../fixtures/test-metadata.generated';\n\n")
	b.WriteString("test.describe('Relay Test Management export', () => {\n")
	for i, c := range cases {
		req := c.Request
		fmt.Fprintf(&b, "  test(%s, async ({ request }, testInfo) => {\n", js(c.Test.Name))
		b.WriteString("    const service = new ApiService(request, relaySession);\n")
		fmt.Fprintf(&b, "    const result = await service.fetch(relayRequests[%d], testInfo);\n", i)
		b.WriteString("    const bodyText = await readResponseBody(result.response);\n")
		if needsJSON(req.Assertions) || hasScriptTests(req.Scripts) {
			b.WriteString("    const jsonBody = () => parseJSONBody(bodyText);\n")
		}
		b.WriteString("    try {\n")
		for j, a := range req.Assertions {
			writeAssertionStep(&b, j+1, a, "      ")
		}
		if hasScriptTests(req.Scripts) {
			b.WriteString("      const scriptResults = runPostmanTests(relayRequests[" + fmt.Sprint(i) + "].scripts?.tests, {\n")
			b.WriteString("        bodyText,\n")
			b.WriteString("        status: result.response.status(),\n")
			b.WriteString("        statusText: result.response.statusText(),\n")
			b.WriteString("        durationMs: result.durationMs,\n")
			b.WriteString("        headers: result.response.headers(),\n")
			b.WriteString("      }, relaySession, relayRequests[" + fmt.Sprint(i) + "].vars || {});\n")
			b.WriteString("      for (let scriptIndex = 0; scriptIndex < scriptResults.length; scriptIndex++) {\n")
			b.WriteString("        const scriptResult = scriptResults[scriptIndex];\n")
			b.WriteString("        await relayStep(testInfo, `Script test ${scriptIndex + 1}: ${scriptResult.name}`, 'script', undefined, async () => {\n")
			b.WriteString("          if (!scriptResult.passed) throw new Error(scriptResult.error || 'script test failed');\n")
			b.WriteString("        });\n")
			b.WriteString("      }\n")
		}
		b.WriteString("    } finally {\n")
		b.WriteString("      await test.step('[XRAY-STEP-N] Attach request body and response for diagnostics', async () => {\n")
		b.WriteString("        await attachRequestResponse(testInfo, result.response.status(), bodyText, relayRequests[" + fmt.Sprint(i) + "].body);\n")
		b.WriteString("      });\n")
		b.WriteString("    }\n")
		b.WriteString("  });\n")
	}
	b.WriteString("});\n")
	return b.String()
}

func writeAssertionStep(b *strings.Builder, n int, a dsl.Assertion, indent string) {
	name := fmt.Sprintf("Assertion %d: %s", n, a.Type)
	expected := assertionExpected(a)
	fmt.Fprintf(b, "%sawait relayStep(testInfo, %s, %s, %s, async () => {\n", indent, js(name), js(a.Type), js(expected))
	line := indent + "  "
	switch a.Type {
	case "status":
		if a.Op == "is2xx" {
			fmt.Fprintf(b, "%sexpect(result.response.status()).toBeGreaterThanOrEqual(200);\n", line)
			fmt.Fprintf(b, "%sexpect(result.response.status()).toBeLessThan(300);\n", line)
		} else if a.Op == "oneof" {
			fmt.Fprintf(b, "%sexpect(%s).toContain(result.response.status());\n", line, jsArray(expList(a)))
		} else {
			fmt.Fprintf(b, "%sexpect(result.response.status()).toBe(%s);\n", line, tsValue(firstNonNil(a.Exp, a.Equals)))
		}
	case "jsonpath", "json":
		acc, err := jsAccessor(a.Path)
		if err != nil {
			fmt.Fprintf(b, "%sthrow new Error(%s);\n", line, js(err.Error()))
		} else if a.Op == "exists" {
			fmt.Fprintf(b, "%sexpect((jsonBody() as any)%s).toBeDefined();\n", line, acc)
		} else if a.Op == "gt" {
			fmt.Fprintf(b, "%sexpect((jsonBody() as any)%s).toBeGreaterThan(%s);\n", line, acc, tsValue(a.Exp))
		} else if a.Op == "lengthGt" {
			fmt.Fprintf(b, "%sexpect((jsonBody() as any)%s.length).toBeGreaterThan(%s);\n", line, acc, tsValue(a.Exp))
		} else {
			fmt.Fprintf(b, "%sexpect((jsonBody() as any)%s).toEqual(%s);\n", line, acc, tsValue(firstNonNil(a.Exp, a.Equals)))
		}
	case "header":
		ref := fmt.Sprintf("result.response.headers()[%s]", js(strings.ToLower(a.Name)))
		if a.Op == "exists" {
			fmt.Fprintf(b, "%sexpect(%s).toBeDefined();\n", line, ref)
		} else if a.Op == "contains" || a.Contains != "" {
			fmt.Fprintf(b, "%sexpect(%s).toContain(%s);\n", line, ref, tsValue(firstNonNil(a.Exp, a.Contains)))
		} else {
			fmt.Fprintf(b, "%sexpect(%s).toBe(%s);\n", line, ref, tsValue(firstNonNil(a.Exp, a.Equals)))
		}
	case "contains", "text":
		if a.Op == "notcontains" {
			fmt.Fprintf(b, "%sexpect(bodyText).not.toContain(%s);\n", line, tsValue(firstNonNil(a.Exp, a.Contains)))
		} else {
			fmt.Fprintf(b, "%sexpect(bodyText).toContain(%s);\n", line, tsValue(firstNonNil(a.Exp, a.Contains)))
		}
	case "max_ms", "timing":
		limit := a.MaxMs
		if v, ok := expInt(a.Exp); ok {
			limit = v
		}
		fmt.Fprintf(b, "%sexpect(result.durationMs).toBeLessThanOrEqual(%d);\n", line, limit)
	default:
		fmt.Fprintf(b, "%sthrow new Error(%s);\n", line, js("unsupported assertion type "+a.Type))
	}
	fmt.Fprintf(b, "%s});\n", indent)
}

func writeStringMapTS(b *strings.Builder, name string, vals map[string]string, indent int) {
	if len(vals) == 0 {
		return
	}
	pad := strings.Repeat(" ", indent)
	fmt.Fprintf(b, "%s%s: {\n", pad, name)
	for _, k := range sortedStringKeys(vals) {
		fmt.Fprintf(b, "%s  %s: %s,\n", pad, js(k), js(vals[k]))
	}
	fmt.Fprintf(b, "%s},\n", pad)
}

func writeStringSliceTS(b *strings.Builder, name string, vals []string, indent int) {
	if len(vals) == 0 {
		return
	}
	pad := strings.Repeat(" ", indent)
	fmt.Fprintf(b, "%s%s: %s,\n", pad, name, jsStringArray(vals))
}

func writeAuthTS(b *strings.Builder, auth *dsl.Auth) {
	if auth == nil {
		return
	}
	fmt.Fprintf(b, "    auth: {\n")
	if auth.Type != "" {
		fmt.Fprintf(b, "      type: %s,\n", js(auth.Type))
	}
	if auth.Token != "" {
		fmt.Fprintf(b, "      token: %s,\n", js(auth.Token))
	}
	if auth.Username != "" {
		fmt.Fprintf(b, "      username: %s,\n", js(auth.Username))
	}
	if auth.Password != "" {
		fmt.Fprintf(b, "      password: %s,\n", js(auth.Password))
	}
	if auth.Key != "" {
		fmt.Fprintf(b, "      key: %s,\n", js(auth.Key))
	}
	if auth.Value != "" {
		fmt.Fprintf(b, "      value: %s,\n", js(auth.Value))
	}
	if auth.In != "" {
		fmt.Fprintf(b, "      in: %s,\n", js(auth.In))
	}
	fmt.Fprintf(b, "    },\n")
}

func writeScriptsTS(b *strings.Builder, scripts *dsl.Scripts) {
	if scripts == nil || (strings.TrimSpace(scripts.PreRequest) == "" && strings.TrimSpace(scripts.Tests) == "") {
		return
	}
	fmt.Fprintf(b, "    scripts: {\n")
	if strings.TrimSpace(scripts.PreRequest) != "" {
		fmt.Fprintf(b, "      preRequest: %s,\n", js(scripts.PreRequest))
	}
	if strings.TrimSpace(scripts.Tests) != "" {
		fmt.Fprintf(b, "      tests: %s,\n", js(scripts.Tests))
	}
	fmt.Fprintf(b, "    },\n")
}

func mergeMaps(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func needsJSON(assertions []dsl.Assertion) bool {
	for _, a := range assertions {
		if a.Type == "json" || a.Type == "jsonpath" || a.Type == "forall" || a.Type == "exists" || a.Type == "count" {
			return true
		}
	}
	return false
}

func hasScriptTests(s *dsl.Scripts) bool {
	return s != nil && strings.TrimSpace(s.Tests) != ""
}

var pmTestName = regexp.MustCompile(`pm\.test\(\s*["']([^"']+)["']`)

func scriptTestNames(s *dsl.Scripts) []string {
	if s == nil || strings.TrimSpace(s.Tests) == "" {
		return nil
	}
	var out []string
	for _, m := range pmTestName.FindAllStringSubmatch(s.Tests, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		out = append(out, "Script assertion")
	}
	return out
}

func assertionExpected(a dsl.Assertion) string {
	switch a.Type {
	case "status":
		return "status " + fmt.Sprint(firstNonNil(a.Exp, a.Equals))
	case "json", "jsonpath":
		return a.Path + " " + fmt.Sprint(firstNonNil(a.Exp, a.Equals))
	case "header":
		return a.Name + " " + fmt.Sprint(firstNonNil(a.Exp, firstNonNil(a.Equals, a.Contains)))
	case "contains", "text":
		return "body contains " + fmt.Sprint(firstNonNil(a.Exp, a.Contains))
	case "max_ms", "timing":
		return fmt.Sprintf("duration <= %dms", a.MaxMs)
	default:
		return a.Type
	}
}

func firstNonNil(a, b any) any {
	if a == nil {
		return b
	}
	if s, ok := a.(string); ok && s == "" {
		return b
	}
	return a
}

func expInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

func expList(a dsl.Assertion) []any {
	v := firstNonNil(a.Exp, a.Equals)
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	case string:
		var out []any
		for _, p := range strings.Split(x, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func jsAccessor(path string) (string, error) {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "$") {
		return "", fmt.Errorf("jsonpath must start with $: %q", path)
	}
	p = p[1:]
	var b strings.Builder
	for len(p) > 0 {
		switch {
		case strings.HasPrefix(p, "."):
			p = p[1:]
			end := strings.IndexAny(p, ".[")
			if end == -1 {
				end = len(p)
			}
			field := p[:end]
			p = p[end:]
			if isJSIdent(field) {
				b.WriteString("." + field)
			} else {
				fmt.Fprintf(&b, "[%s]", js(field))
			}
		case strings.HasPrefix(p, "["):
			end := strings.Index(p, "]")
			if end == -1 {
				return "", fmt.Errorf("unclosed [ in %q", path)
			}
			idx := p[1:end]
			p = p[end+1:]
			if len(idx) >= 2 && (idx[0] == '"' || idx[0] == '\'') {
				fmt.Fprintf(&b, "[%s]", js(idx[1:len(idx)-1]))
			} else {
				fmt.Fprintf(&b, "[%s]", idx)
			}
		default:
			return "", fmt.Errorf("unexpected %q in %q", p[:1], path)
		}
	}
	return b.String(), nil
}

func isJSIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r == '$' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(vals []string) []string {
	out := append([]string(nil), vals...)
	sort.Strings(out)
	return out
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func tsType(v any) string {
	switch x := v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case []any:
		if len(x) == 0 {
			return "unknown[]"
		}
		return tsType(x[0]) + "[]"
	case map[string]any:
		return "Record<string, unknown>"
	default:
		return "unknown"
	}
}

func tsProp(s string) string {
	if isJSIdent(s) {
		return s
	}
	return js(s)
}

func tsValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return js(fmt.Sprint(v))
	}
	return string(b)
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func jsArray(vals []any) string {
	b, _ := json.Marshal(vals)
	return string(b)
}

func jsStringArray(vals []string) string {
	b, _ := json.Marshal(vals)
	return string(b)
}

var nonSlug = regexp.MustCompile(`[^A-Za-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if s == "" {
		return "relay"
	}
	return s
}

func pascal(s string) string {
	parts := nonSlug.Split(strings.TrimSpace(s), -1)
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			b.WriteString(p[1:])
		}
	}
	if b.Len() == 0 {
		return "Relay"
	}
	return b.String()
}

func envKey(s string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s))
}
