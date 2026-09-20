import { defineConfig } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(frontendRoot, "..");
// Cross-app navigation must stay inside this isolated preview, never jump to
// the operator's daily development servers or their selected remote backend.
const frontendEnvironment = {
  CPA_DEV_PROXY_TARGET: "http://127.0.0.1:8896",
  VITE_DEV_ADMIN_ORIGIN: "http://127.0.0.1:5193",
  VITE_DEV_USAGE_ORIGIN: "http://127.0.0.1:5194",
  VITE_DEV_PORTAL_ORIGIN: "http://127.0.0.1:5192"
};
const previewRoot = "frontend/node_modules/.cache/cpa-browser-preview";
const webPreviewCommand = (port: number) => `go run ./cmd/web --address 127.0.0.1:${port} --admin-target http://127.0.0.1:8896 --portal-root ${previewRoot}/portal --admin-root ${previewRoot}/admin --usage-root ${previewRoot}/usage --log-level error`;

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: {
    timeout: 10_000,
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      maxDiffPixelRatio: 0.005
    }
  },
  // Contexts and route overrides are per test; the preview serves read-only fixtures.
  fullyParallel: true,
  workers: Number(process.env.CPAP_E2E_WORKERS || 2),
  reporter: [["list"], ["html", { open: "never" }], ["json", { outputFile: "test-results/browser-timings.json" }]],
  use: {
    baseURL: "http://127.0.0.1:5193",
    browserName: "chromium",
    locale: "zh-CN",
    // Existing behavior and visual baselines explicitly cover the Chinese locale.
    // language.spec.ts starts without preferences and covers the English default.
    storageState: { cookies: [{ name: "cpa-ui-language", value: "zh-CN", domain: "127.0.0.1", path: "/", expires: -1, httpOnly: false, secure: false, sameSite: "Lax" }], origins: [] },
    timezoneId: "Asia/Shanghai",
    colorScheme: "light",
    screenshot: "only-on-failure",
    // DOM tracing on every successful action is expensive at high concurrency.
    // Failure screenshots and visual assertions remain enabled; opt into traces.
    trace: process.env.CPAP_E2E_TRACE === "1" ? "retain-on-failure" : "off"
  },
  webServer: [
    {
      command: "npm --prefix frontend run build:browser-preview && go run ./cmd/test-preview --address 127.0.0.1:8896 --root .",
      env: frontendEnvironment,
      cwd: repositoryRoot,
      url: "http://127.0.0.1:8896/healthz",
      reuseExistingServer: false,
      timeout: 120_000
    },
    {
      command: webPreviewCommand(5193),
      cwd: repositoryRoot,
      url: "http://127.0.0.1:5193/admin/",
      reuseExistingServer: false,
      timeout: 30_000
    },
    {
      command: webPreviewCommand(5194),
      cwd: repositoryRoot,
      url: "http://127.0.0.1:5194/usage/",
      reuseExistingServer: false,
      timeout: 30_000
    },
    {
      command: webPreviewCommand(5192),
      cwd: repositoryRoot,
      url: "http://127.0.0.1:5192/",
      reuseExistingServer: false,
      timeout: 30_000
    }
  ]
});
