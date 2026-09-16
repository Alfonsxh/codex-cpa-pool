import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { closeSync, existsSync, fsyncSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const stablePattern = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
const shaPattern = /^[a-f0-9]{64}$/;
const components = ["control", "web", "gateway", "edge"];
const hash = value => createHash("sha256").update(value).digest("hex");
const requireValue = (condition, message) => { if (!condition) throw new Error(message); };
export const stable = value => typeof value === "string" && value.length < 100 && stablePattern.test(value);
export function atLeast(version, minimum) {
  const a = version.slice(1).split(".").map(BigInt), b = minimum.slice(1).split(".").map(BigInt);
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return a[i] > b[i];
  return true;
}

// Never propagate subprocess error objects: Telegram URLs contain the Bot Token.
export function command(program, args, options = {}) {
  try {
    return execFileSync(program, args, { cwd: root, encoding: "utf8", timeout: 120_000,
      maxBuffer: 8 << 20, stdio: ["pipe", "pipe", "pipe"], ...options });
  } catch { throw new Error(`${program} 操作失败；请检查网络、凭据及工具状态`); }
}
function privatePath(file, directory = false) {
  const stat = lstatSync(file);
  requireValue(!stat.isSymbolicLink() && (directory ? stat.isDirectory() : stat.isFile()) &&
    (stat.mode & 0o077) === 0 && (process.getuid === undefined || stat.uid === process.getuid()),
  "通知配置与回执须为当前用户所有的私有普通文件/目录（文件 0600，目录 0700）");
}
export function readConfig(configFile, repo) {
  if (!existsSync(configFile)) return null;
  privatePath(path.dirname(configFile), true);
  privatePath(configFile);
  // Keep configuration out of every Git checkout, including ignored paths.
  let inGit = false;
  try { execFileSync("git", ["-C", realpathSync(path.dirname(configFile)), "rev-parse", "--show-toplevel"], { stdio: "ignore" }); inGit = true; } catch { /* not a checkout */ }
  requireValue(!inGit, "通知配置必须位于 Git 工作区之外");
  const config = JSON.parse(readFileSync(configFile, "utf8"));
  if (config.enabled === false) return null;
  requireValue(config.enabled === true && config.repository === repo && Number.isSafeInteger(config.repository_id) && config.repository_id > 0 &&
    /^-[1-9][0-9]*$/.test(String(config.chat_id)) && stable(config.min_version) && /^[A-Za-z0-9_]+bot$/i.test(config.bot_username), "通知配置字段缺失或与发布仓库不匹配");
  if (config.proxy_url !== undefined) {
    const proxy = new URL(config.proxy_url);
    requireValue(["http:", "https:"].includes(proxy.protocol) && !proxy.username && !proxy.password && proxy.pathname === "/" && !proxy.search && !proxy.hash, "通知代理地址无效");
  }
  return { ...config, chat_id: String(config.chat_id), directory: path.dirname(configFile) };
}
function readToken(config) {
  const file = path.join(config.directory, "bot-token");
  privatePath(file);
  const token = readFileSync(file, "utf8").trim();
  requireValue(/^[0-9]+:[A-Za-z0-9_-]+$/.test(token), "Bot Token 格式无效");
  return token;
}
const curlQuote = value => `"${String(value).replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\r/g, "\\r").replace(/\n/g, "\\n")}"`;
export function telegramAPI(token, run = command, proxy) {
  return (method, payload) => {
    requireValue(["getMe", "getChat", "getChatMember", "sendMessage", "editMessageText"].includes(method), "不支持的 Telegram 操作");
    const input = ["silent", "show-error", "max-time = 25", "connect-timeout = 10", "max-filesize = 65536",
      `url = ${curlQuote(`https://api.telegram.org/bot${token}/${method}`)}`, 'request = "POST"',
      'header = "Content-Type: application/json"', `data = ${curlQuote(JSON.stringify(payload))}`,
      ...(proxy ? [`proxy = ${curlQuote(proxy)}`] : [])].join("\n");
    // URL and token travel only through stdin, never process arguments or logs.
    try { return JSON.parse(run("curl", ["--disable", "--config", "-"], { input, timeout: 30_000, maxBuffer: 65536 })); }
    catch { throw new Error("Telegram 请求未获得可确认的结果"); }
  };
}
export function checkDestination(config, api) {
  const me = api("getMe", {}), chat = api("getChat", { chat_id: config.chat_id });
  requireValue(me.ok === true && me.result?.username === config.bot_username && me.result?.is_bot === true, "发布机器人身份不匹配");
  requireValue(chat.ok === true && String(chat.result?.id) === config.chat_id && ["group", "supergroup", "channel"].includes(chat.result.type), "通知目标不存在或类型不匹配");
  const member = api("getChatMember", { chat_id: config.chat_id, user_id: me.result.id });
  const m = member.result;
  requireValue(member.ok === true && (m?.status === "creator" ||
    (m?.status === "administrator" && (chat.result.type !== "channel" || m.can_post_messages === true)) ||
    (chat.result.type !== "channel" && m?.status === "member" && chat.result.permissions?.can_send_messages === true)), "机器人没有向目标发送消息的权限");
  return { botID: me.result.id, chat: chat.result };
}
function section(body, title) {
  const parts = body.split(/^## /m).slice(1).filter(part => part.split(/\r?\n/, 1)[0].trim() === title);
  requireValue(parts.length === 1, `Release 必须包含唯一的“${title}”段落`);
  const newline = parts[0].indexOf("\n");
  const text = newline < 0 ? "" : parts[0].slice(newline + 1).trim();
  requireValue(text.length > 0, `Release 的“${title}”不能为空`);
  return text;
}
const escapeHTML = value => String(value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
function formatSection(text) {
  // Only render authored emphasis and inline commands. Raw HTML stays literal.
  return text.replace(/\[([^\]]+)\]\((https:\/\/[^\s)]+)\)/g, "$1 ($2)")
    .replace(/^[ \t]*[-*] +/gm, "• ").split(/(`[^`\n]+`|\*\*[^*\n]+\*\*)/g).map(part => {
      if (part.startsWith("`") && part.endsWith("`") && part.length > 2) return `<code>${escapeHTML(part.slice(1, -1))}</code>`;
      if (part.startsWith("**") && part.endsWith("**") && part.length > 4) return `<b>${escapeHTML(part.slice(2, -2))}</b>`;
      return escapeHTML(part);
    }).join("");
}
export function renderMessage(release, repo) {
  const text = `🎉 <b>CCPA ${escapeHTML(release.tag_name)} 正式发布</b>\n\n<b>本次更新</b>\n\n${formatSection(section(release.body || "", "社群摘要"))}\n\n<b>升级提示</b>\n\n${formatSection(section(release.body || "", "升级提示"))}`;
  requireValue(text.length <= 3800, "群通知过长；请精简 Release 的社群摘要和升级提示");
  return { text, parse_mode: "HTML", link_preview_options: { is_disabled: true }, reply_markup: { inline_keyboard: [[
    { text: "发布详情", url: `https://github.com/${repo}/releases/tag/${release.tag_name}` },
    { text: "安装升级", url: `https://github.com/${repo}/blob/main/docs/upgrade.md` }
  ]] } };
}
export function eligible(release, version) {
  return stable(version) && release.tag_name === version && release.draft === false && release.prerelease === false && Boolean(release.published_at);
}
export function verifyAssets({ release, repo, version, revision, imagePrefix, directory, run = command }) {
  const names = [`codex-cpa-pool-${version}.tar.gz`, `release-${version}.json`, `release-${version}.env`, "run.sh"];
  for (const name of [...names, "SHA256SUMS"]) {
    const assets = (release.assets || []).filter(asset => asset.name === name);
    requireValue(assets.length === 1 && assets[0].state === "uploaded" && assets[0].size > 0 && assets[0].size <= (128 << 20), `发布附件缺失或无效：${name}`);
  }
  // The deployment archive is ~12 MiB and slow links can need several minutes;
  // the default 120s command budget would report a bogus tool failure.
  run("gh", ["release", "download", version, "--repo", repo, "--dir", directory, ...[...names, "SHA256SUMS"].flatMap(name => ["--pattern", name])], { timeout: 900_000 });
  const checksums = new Map();
  for (const line of readFileSync(path.join(directory, "SHA256SUMS"), "utf8").trim().split(/\r?\n/)) {
    const match = /^([a-f0-9]{64}) [ *]([^/\\]+)$/.exec(line);
    requireValue(match && names.includes(match[2]) && !checksums.has(match[2]), "SHA256SUMS 格式、名称或重复条目无效");
    checksums.set(match[2], match[1]);
  }
  requireValue(checksums.size === names.length, "SHA256SUMS 缺少必要附件");
  for (const name of [...names, "SHA256SUMS"]) {
    const stat = lstatSync(path.join(directory, name));
    const asset = release.assets.find(a => a.name === name);
    requireValue(stat.isFile() && !stat.isSymbolicLink() && stat.size === asset.size, `附件大小不匹配：${name}`);
    const digest = hash(readFileSync(path.join(directory, name)));
    requireValue((name === "SHA256SUMS" || digest === checksums.get(name)) && (!asset.digest || asset.digest === `sha256:${digest}`), `附件 SHA-256 不匹配：${name}`);
  }
  const descriptor = JSON.parse(readFileSync(path.join(directory, names[1]), "utf8"));
  requireValue(descriptor.schema_version === 1 && descriptor.release_version === version && descriptor.archive_name === names[0] &&
    descriptor.revision === revision && /^[a-f0-9]{40}$/.test(revision) &&
    /^[A-Za-z0-9.-]+(?::[0-9]+)?\/[A-Za-z0-9._/-]+$/.test(descriptor.image_prefix) &&
    (!imagePrefix || descriptor.image_prefix === imagePrefix), "发布描述与版本、提交或镜像仓库不一致");
  run("go", ["run", "./cmd/releasectl", "archive", "verify", path.join(directory, names[0])]);
  const manifest = JSON.parse(run("tar", ["-xOf", path.join(directory, names[0]), "release-manifest.json"]));
  requireValue(manifest.version === 1, "归档清单版本无效");
  requireValue(run("tar", ["-xOf", path.join(directory, names[0]), "scripts/run.sh"]) === readFileSync(path.join(directory, "run.sh"), "utf8"), "安装脚本与归档不一致");
  const env = readFileSync(path.join(directory, names[2]), "utf8");
  const envLines = env.trim().split(/\r?\n/);
  requireValue(envLines.length === 7 && new Set(envLines.map(line => line.split("=", 1)[0])).size === 7 &&
    envLines.includes(`CPAP_RELEASE_VERSION=${version}`) && envLines.includes(`CPAP_RELEASE_REVISION=${revision}`) &&
    envLines.includes(`CPAP_RELEASE_ARCHIVE=${names[0]}`), "部署环境的版本、提交或归档不一致");
  for (const component of components) {
    const entry = descriptor.components?.[component], digest = entry?.source_sha256;
    requireValue(shaPattern.test(digest || "") && manifest.components?.[component]?.source_sha256 === digest &&
      entry.image === `${descriptor.image_prefix}/codex-cpa-${component}:sha256-${digest}` &&
      envLines.includes(`CPAP_${component.toUpperCase()}_IMAGE=${entry.image}`), "镜像描述与归档或部署环境不一致");
    const metadata = JSON.parse(run("docker", ["buildx", "imagetools", "inspect", "--format", "{{json .}}", entry.image]));
    const labels = metadata.image?.config?.Labels;
    requireValue(/^sha256:[a-f0-9]{64}$/.test(metadata.manifest?.digest || "") && labels?.["io.codex-cpa.component"] === component &&
      labels?.["io.codex-cpa.component-digest"] === digest && labels?.["io.codex-cpa.source-digest"] === digest, "远端镜像身份与发布描述不一致");
  }
  return descriptor;
}
function atomicJSON(file, value) {
  const temporary = `${file}.${process.pid}.tmp`;
  const fd = openSync(temporary, "wx", 0o600);
  try { writeFileSync(fd, JSON.stringify(value, null, 2) + "\n"); fsyncSync(fd); } finally { closeSync(fd); }
  renameSync(temporary, file);
  const dir = openSync(path.dirname(file), "r");
  try { fsyncSync(dir); } finally { closeSync(dir); }
}
export function receiptPath(config, version) {
  return path.join(config.directory, "deliveries", hash(`${config.repository_id}:${version}:${config.chat_id}`) + ".json");
}
export function readReceipt(file) {
  if (!existsSync(file)) return null;
  privatePath(file);
  return JSON.parse(readFileSync(file, "utf8"));
}
export function deliver({ config, release, message, destination, action = "send", api, sleep = ms => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms) }) {
  const file = receiptPath(config, release.tag_name), directory = path.dirname(file);
  mkdirSync(directory, { recursive: true, mode: 0o700 }); privatePath(directory, true);
  const lock = `${file}.lock`;
  try { mkdirSync(lock, { mode: 0o700 }); } catch { throw new Error("此版本的通知已有进程处理；若为遗留锁，请先核对进程和回执"); }
  try {
    atomicJSON(path.join(lock, "owner.json"), { pid: process.pid, started_at: new Date().toISOString() });
    const previous = readReceipt(file);
    if (previous) {
      requireValue(previous.schema === 1 && ["sent", "pending", "unknown", "failed"].includes(previous.status) &&
        Number.isSafeInteger(previous.attempts) && previous.attempts > 0 &&
        (previous.status !== "sent" || Number.isSafeInteger(previous.message_id)), "通知回执格式无效，必须人工核对");
      requireValue(previous.release_id === release.id && previous.repository_id === config.repository_id && previous.chat_id === config.chat_id && previous.bot_id === destination.botID, "已有回执的 Release、仓库、目标或机器人身份不匹配");
      requireValue(!["pending", "unknown"].includes(previous.status), "上次发送结果不明，必须先核对群消息及回执，禁止盲目重发");
    }
    const digest = hash(JSON.stringify(message));
    if (previous?.status === "sent" && (action === "send" || previous.content_sha256 === digest)) return { ...previous, reused: true };
    requireValue(action !== "edit" || Number.isSafeInteger(previous?.message_id), "只有已有成功消息才能编辑");
    let record = { schema: 1, repository_id: config.repository_id, repository: config.repository, release_id: release.id,
      version: release.tag_name, chat_id: config.chat_id, bot_id: destination.botID, content_sha256: digest,
      attempts: previous?.attempts || 0, ...(previous?.message_id ? { message_id: previous.message_id } : {}) };
    const save = status => { record = { ...record, status, updated_at: new Date().toISOString() }; atomicJSON(file, record); };
    for (let attempt = 0; attempt < 3; attempt++) {
      record.attempts++; save("pending");
      let response;
      try { response = api(action === "edit" ? "editMessageText" : "sendMessage", { chat_id: config.chat_id, ...message,
        ...(action === "edit" ? { message_id: previous.message_id } : {}) }); }
      catch { save("unknown"); throw new Error("Telegram 发送结果不明；请核对群消息，回执已保留，禁止自动重发"); }
      if (response?.ok === true && Number.isSafeInteger(response.result?.message_id) && String(response.result?.chat?.id) === config.chat_id) {
        record.message_id = response.result.message_id;
        const username = destination.chat.username;
        if (/^[A-Za-z0-9_]+$/.test(username || "")) record.message_url = `https://t.me/${username}/${record.message_id}`;
        save("sent"); return record;
      }
      // A lost successful edit can safely be reconciled by Telegram's unchanged response.
      if (action === "edit" && response?.ok === false && response.error_code === 400 && /^Bad Request: message is not modified/.test(response.description || "")) {
        record.message_url = previous.message_url; save("sent"); return record;
      }
      if (response?.ok !== false || !Number.isInteger(response.error_code) || response.error_code >= 500) { save("unknown"); throw new Error("Telegram 响应无法确认发送结果，待人工核对"); }
      save("failed");
      const delay = response.parameters?.retry_after;
      if (response.error_code === 429 && Number.isInteger(delay) && delay >= 0 && delay <= 60 && attempt < 2) { sleep(delay * 1000); continue; }
      throw new Error(`Telegram 拒绝通知（错误码 ${response.error_code}）；修正后可重试`);
    }
  } finally { rmSync(lock, { recursive: true, force: true }); }
}
function resolveRevision(repo, version, run) {
  let ref = JSON.parse(run("gh", ["api", `repos/${repo}/git/ref/tags/${version}`])).object;
  for (let i = 0; ref?.type === "tag" && i < 4; i++) ref = JSON.parse(run("gh", ["api", `repos/${repo}/git/tags/${ref.sha}`])).object;
  requireValue(ref?.type === "commit" && /^[a-f0-9]{40}$/.test(ref.sha), "Release Tag 未指向有效提交");
  return ref.sha;
}
export function notifyRelease({ action, repo, version, revision, imagePrefix, configFile, run = command, apiFactory = telegramAPI }) {
  requireValue(["check", "send", "edit", "preview", "status"].includes(action), "动作必须是 check、send、edit、preview 或 status");
  requireValue(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repo), "仓库格式无效");
  if (!stable(version)) return { status: "skipped", reason: "非正式版本" };
  const config = readConfig(configFile, repo);
  if (!config) return { status: "disabled", reason: "未启用 Telegram 发布通知" };
  if (!atLeast(version, config.min_version)) return { status: "skipped", reason: "早于启用版本，不补发历史 Release" };
  if (action === "status") return readReceipt(receiptPath(config, version)) || { status: "not_sent" };
  const api = apiFactory(readToken(config), run, config.proxy_url);
  if (action === "check") {
    checkDestination(config, api);
    const draft = JSON.parse(run("gh", ["release", "view", version, "--repo", repo, "--json", "body,isPrerelease"]));
    requireValue(draft.isPrerelease === false, "正式版 Draft 不得标记为预发布");
    renderMessage({ tag_name: version, body: draft.body }, repo);
    return { status: "ready", version };
  }
  const repository = JSON.parse(run("gh", ["api", `repos/${repo}`]));
  requireValue(repository.private === false && repository.id === config.repository_id, "发布仓库身份不匹配或尚未公开");
  const release = JSON.parse(run("gh", ["api", `repos/${repo}/releases/tags/${version}`]));
  if (!eligible(release, version)) return { status: "skipped", reason: "Release 尚未公开或标记为预发布" };
  requireValue(Number.isSafeInteger(release.id) && release.id > 0, "Release ID 无效");
  const resolvedRevision = resolveRevision(repo, version, run);
  requireValue(!revision || revision === resolvedRevision, "Release Tag 与本次发布提交不一致");
  const message = renderMessage(release, repo);
  const directory = mkdtempSync(path.join(os.tmpdir(), "cpap-telegram-release-"));
  try { verifyAssets({ release, repo, version, revision: resolvedRevision, imagePrefix, directory, run }); }
  finally { rmSync(directory, { recursive: true, force: true }); }
  if (action === "preview") return { status: "preview", version, ...message };
  const destination = checkDestination(config, api);
  return deliver({ config, release, message, destination, action, api });
}
// macOS temporary worktrees may enter via /var while ESM resolves /private/var.
if (process.argv[1] && existsSync(process.argv[1]) && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [action, ...args] = process.argv.slice(2), options = {};
    for (let i = 0; i < args.length; i += 2) {
      requireValue(["--repo", "--version", "--revision", "--image-prefix", "--config"].includes(args[i]) && args[i + 1] && !options[args[i]], "通知命令参数无效");
      options[args[i]] = args[i + 1];
    }
    const configFile = options["--config"] || process.env.CPAP_TELEGRAM_CONFIG || path.join(process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config"), "codex-cpa-pool", "telegram-release", "config.json");
    console.log(JSON.stringify(notifyRelease({ action, repo: options["--repo"], version: options["--version"], revision: options["--revision"], imagePrefix: options["--image-prefix"], configFile }), null, 2));
  } catch (error) {
    // JSON/filesystem/parser errors can include input fragments. Log only our own
    // curated errors; never leak malformed private config or subprocess objects.
    const safe = error instanceof Error && /[\u4e00-\u9fff]/.test(error.message) && !error.message.includes("https://") && error.name === "Error";
    console.error(safe ? error.message : "发布通知失败，请检查私有配置、回执及发布产物"); process.exitCode = 1;
  }
}
