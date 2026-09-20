import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { cpSync, lstatSync, mkdirSync, readFileSync, readdirSync, realpathSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const hash = (value) => createHash("sha256").update(value).digest("hex");
const execute = (command, args, cwd, capture = false) => execFileSync(command, args, {
  cwd, encoding: "utf8", stdio: capture ? ["ignore", "pipe", "inherit"] : "inherit"
});

// Hash paths, modes and bytes; cached output must never contain symlinks.
export function artifactDigest(root) {
  const digest = createHash("sha256");
  function walk(relative) {
    const file = path.join(root, relative);
    const stat = lstatSync(file);
    if (stat.isSymbolicLink()) throw new Error(`发布产物包含符号链接：${relative}`);
    if (stat.isDirectory()) {
      for (const name of readdirSync(file).sort()) walk(path.join(relative, name));
    } else if (stat.isFile()) {
      digest.update(`${relative}\0${stat.mode & 0o777}\0${stat.size}\0`);
      digest.update(readFileSync(file));
    } else throw new Error(`发布产物类型无效：${relative}`);
  }
  walk("");
  for (const app of ["admin", "portal", "usage"]) {
    if (!lstatSync(path.join(root, app, "index.html")).isFile()) throw new Error(`缺少 ${app} 产物`);
  }
  return digest.digest("hex");
}

export function validationIdentity(root, platform, run = execute) {
  const read = (cmd, args) => run(cmd, args, root, true).trim();
  if (read("git", ["status", "--porcelain", "--untracked-files=normal"])) throw new Error("发布验收只接受干净的固定提交");
  const require = createRequire(path.join(root, "frontend", "package.json"));
  const browser = require("@playwright/test").chromium.executablePath();
  const environment = Object.fromEntries([
    "CI", "TZ", "LANG", "LC_ALL", "NODE_ENV", "NODE_OPTIONS", "GOFLAGS", "GOEXPERIMENT", "GODEBUG", "CGO_ENABLED",
    "CPAP_E2E_WORKERS", "CPAP_E2E_TRACE", "PLAYWRIGHT_BROWSERS_PATH"
  ].concat(Object.keys(process.env).filter((key) => /^(VITE_|CPA_)/.test(key))).sort().map((key) => [key, process.env[key] ?? ""]));
  return hash(JSON.stringify({
    schema: 1, tree: read("git", ["rev-parse", "HEAD^{tree}"]), platform,
    node: process.version, os: process.platform, arch: process.arch,
    go: read("go", ["version"]), goEnv: read("go", ["env", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS", "GOEXPERIMENT"]),
    npm: read("npm", ["--version"]), compose: read("docker", ["compose", "version", "--short"]),
    browser: hash(readFileSync(browser)), environment
  }));
}

function readReceipt(directory, key) {
  try {
    if (lstatSync(directory).isSymbolicLink() || lstatSync(path.join(directory, "receipt.json")).isSymbolicLink()) return null;
    const receipt = JSON.parse(readFileSync(path.join(directory, "receipt.json"), "utf8"));
    if (receipt.schema !== 1 || receipt.key !== key || receipt.source !== true ||
        receipt.artifact !== artifactDigest(path.join(directory, "dist"))) return null;
    return receipt;
  } catch { return null; }
}

// run/identity are injected only by the local contract tests, never by CLI flags.
export function validateRelease({ root, cache, platform, checkOnly = false, run = execute, identity = validationIdentity }) {
  const key = identity(root, platform, run);
  const directory = path.join(cache, key);
  const dist = path.join(root, "frontend", "dist");
  if (checkOnly) {
    const receipt = readReceipt(directory, key);
    if (!receipt?.browser || artifactDigest(dist) !== receipt.artifact) throw new Error("前端产物未通过本次源码与工具链的完整验收");
    return receipt;
  }
  mkdirSync(cache, { recursive: true, mode: 0o700 });
  const lock = path.join(cache, `${key}.lock`);
  try { mkdirSync(lock, { mode: 0o700 }); }
  catch (error) {
    if (error.code !== "EEXIST") throw error;
    throw new Error(`同一源码正在验收；如上次进程已退出，请移除遗留锁：${lock}`);
  }
  const started = Date.now();
  try {
    let receipt = readReceipt(directory, key);
    if (receipt) {
      rmSync(dist, { recursive: true, force: true });
      cpSync(path.join(directory, "dist"), dist, { recursive: true });
      console.log("[验收] 复用已通过的源码检查及生产前端产物");
    } else {
      const stage = Date.now();
      run("make", ["-f", "scripts/build.mk", "verify"], root);
      if (identity(root, platform, run) !== key) throw new Error("源码或工具链在验收期间变化");
      receipt = { schema: 1, key, source: true, browser: false, artifact: artifactDigest(dist), sourceSeconds: (Date.now() - stage) / 1000 };
      const pending = `${directory}.pending-${process.pid}`;
      rmSync(pending, { recursive: true, force: true });
      mkdirSync(pending, { mode: 0o700 });
      cpSync(dist, path.join(pending, "dist"), { recursive: true });
      writeFileSync(path.join(pending, "receipt.json"), JSON.stringify(receipt), { mode: 0o600 });
      rmSync(directory, { recursive: true, force: true });
      renameSync(pending, directory);
      console.log(`[验收] 源码检查 ${receipt.sourceSeconds.toFixed(1)} 秒`);
    }
    if (!receipt.browser) {
      const stage = Date.now();
      run("npm", ["--prefix", "frontend", "run", "test:e2e"], root);
      if (identity(root, platform, run) !== key || artifactDigest(dist) !== receipt.artifact) throw new Error("源码或产物在浏览器验收期间变化");
      receipt = { ...receipt, browser: true, browserSeconds: (Date.now() - stage) / 1000 };
      const temporary = path.join(directory, "receipt.pending");
      writeFileSync(temporary, JSON.stringify(receipt), { mode: 0o600 });
      renameSync(temporary, path.join(directory, "receipt.json"));
      console.log(`[验收] 浏览器检查 ${receipt.browserSeconds.toFixed(1)} 秒`);
    } else console.log("[验收] 复用已通过的完整浏览器检查");
    console.log(`[验收] 本次耗时 ${((Date.now() - started) / 1000).toFixed(1)} 秒；记录 ${key.slice(0, 12)}`);
    return receipt;
  } finally { rmSync(lock, { recursive: true, force: true }); }
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  const [root, cache, platform, mode] = process.argv.slice(2);
  if (!root || !cache || !platform || (mode && mode !== "--check")) throw new Error("usage: release-validation.mjs ROOT CACHE PLATFORM [--check]");
  validateRelease({ root: path.resolve(root), cache: path.resolve(cache), platform, checkOnly: mode === "--check" });
}
