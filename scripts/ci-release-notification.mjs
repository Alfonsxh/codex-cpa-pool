import { appendFileSync, existsSync, lstatSync, mkdirSync, realpathSync, renameSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { checkDestination, readConfig, receiptPath, telegramAPI } from "./telegram-release.mjs";

function privatePath(file, directory = false) {
  const s = lstatSync(file);
  if (s.isSymbolicLink() || !(directory ? s.isDirectory() : s.isFile()) || (s.mode & 0o077) ||
      (process.getuid && s.uid !== process.getuid())) throw new Error("Runner 通知目录或文件权限无效");
}

export function prepareNotification({ directory, repo, configJSON, token, seedJSON = "" }) {
  if (!configJSON || !/^[0-9]+:[A-Za-z0-9_-]+$/.test(token || "")) throw new Error("缺少有效的 Telegram 发布 Secrets");
  const configInput = JSON.parse(configJSON);
  if (configInput.enabled !== true || configInput.repository !== repo) throw new Error("Telegram 配置未启用或仓库不匹配");
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  privatePath(directory, true);
  const configFile = path.join(directory, "config.json");
  // Fail before replacing a different destination: its receipts must not be lost.
  if (existsSync(configFile)) {
    const previous = readConfig(configFile, repo);
    if (!previous || ["repository_id", "chat_id", "bot_username"].some(key => String(previous[key]) !== String(configInput[key]))) throw new Error("Runner 已有不同的通知目标");
  }
  for (const [name, value] of [["config.json", JSON.stringify(configInput)], ["bot-token", token]]) {
    const file = path.join(directory, name);
    if (existsSync(file)) privatePath(file);
    const pending = `${file}.${process.pid}.pending`;
    writeFileSync(pending, value, { mode: 0o600, flag: "wx" });
    renameSync(pending, file);
  }
  const config = readConfig(configFile, repo);
  const deliveries = path.join(directory, "deliveries");
  mkdirSync(deliveries, { mode: 0o700, recursive: true });
  privatePath(deliveries, true);
  // One-time workstation receipt migration; existing Runner receipts always win.
  const seeds = seedJSON ? JSON.parse(seedJSON) : [];
  if (!Array.isArray(seeds)) throw new Error("历史通知回执格式无效");
  let imported = 0;
  for (const record of seeds) {
    if (record.repository_id !== config.repository_id || record.repository !== repo || String(record.chat_id) !== config.chat_id ||
        !/^v[0-9]+\.[0-9]+\.[0-9]+$/.test(record.version) || !["sent", "failed", "pending", "unknown"].includes(record.status)) throw new Error("历史通知回执身份无效");
    const file = receiptPath(config, record.version);
    if (existsSync(file)) { privatePath(file); continue; }
    writeFileSync(file, JSON.stringify(record), { mode: 0o600, flag: "wx" });
    imported++;
  }
  return { config, configFile, imported };
}

if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const token = (process.env.CPAP_TELEGRAM_BOT_TOKEN || "").trim();
    const prepared = prepareNotification({
      directory: path.join(os.homedir(), ".config", "codex-cpa-pool", "ci-telegram-release"),
      repo: process.env.GH_REPO, configJSON: process.env.CPAP_TELEGRAM_CONFIG_JSON, token,
      seedJSON: process.env.CPAP_TELEGRAM_RECEIPTS_JSON
    });
    const statusOnly = process.env.OPERATION === "notify" && process.env.NOTIFY_ACTION === "status";
    if (!statusOnly) checkDestination(prepared.config, telegramAPI(token, undefined, prepared.config.proxy_url));
    appendFileSync(process.env.GITHUB_ENV, `CPAP_TELEGRAM_CONFIG=${prepared.configFile}\n`);
    console.log(`Telegram configuration ready; destination ${statusOnly ? "not queried for receipt status" : "verified"}; imported ${prepared.imported} historical receipts. No message sent.`);
  } catch {
    console.error("Runner 通知配置或目标校验失败；检查私有配置、Secrets、回执权限和网络。未发送公告。");
    process.exitCode = 1;
  }
}
