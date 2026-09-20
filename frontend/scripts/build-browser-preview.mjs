import { build } from "vite";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
for (const name of ["ADMIN", "USAGE", "PORTAL"]) {
  const origin = new URL(process.env[`VITE_DEV_${name}_ORIGIN`]);
  if (origin.hostname !== "127.0.0.1" || origin.protocol !== "http:") {
    throw new Error("Browser preview must use isolated loopback origins");
  }
}

// Bundle once, then exercise the real Go Web static/proxy handler. Keep the
// isolated cross-app origins used by browser tests without changing release assets.
for (const [name, config] of [["admin", "vite.config.ts"], ["usage", "vite.usage.config.ts"], ["portal", "vite.portal.config.ts"]]) {
  await build({
    configFile: path.join(root, config),
    mode: "production",
    define: { "import.meta.env.DEV": "true" },
    build: {
      outDir: path.join(root, "node_modules/.cache/cpa-browser-preview", name),
      emptyOutDir: true,
      sourcemap: false
    }
  });
}
