import react from "@vitejs/plugin-react";
import { loadEnv } from "vite";
import { configDefaults, defineConfig } from "vitest/config";

import { adminCodeSplitting } from "./vite.shared.ts";

export default defineConfig(({ mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    base: "/admin/",
    cacheDir: "node_modules/.vite-admin",
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: {
        "/admin/api": proxy(),
        "/branding": proxy(),
        "/portal/assets": proxy(),
        "/site-config.json": proxy()
      }
    },
    build: {
      outDir: "dist/admin",
      emptyOutDir: true,
      sourcemap: true,
      rolldownOptions: { output: { codeSplitting: adminCodeSplitting } }
    },
    test: {
      environment: "jsdom",
      exclude: [...configDefaults.exclude, "e2e/**"],
      // Bound CI concurrency on the shared four-core Runner. Unbounded workers
      // overload Ant Design's layout work; local checks keep one file at a time.
      fileParallelism: Boolean(process.env.CI),
      maxWorkers: process.env.CI ? 2 : 1,
      // The shared ARM CI Runner needs more wall time for Ant Design's layout
      // and user-event work. Keep all assertions and the local 5-second limit.
      testTimeout: process.env.CI ? 15_000 : 5_000,
      setupFiles: "./src/test/setup.ts",
      restoreMocks: true
    }
  };
});
