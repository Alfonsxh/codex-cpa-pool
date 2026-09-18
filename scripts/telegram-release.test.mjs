import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { chmodSync, existsSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import os from "node:os";
import path from "node:path";
import { atLeast, checkDestination, deliver, eligible, notificationOutput, notifyRelease, readConfig, readReceipt, receiptPath, renderMessage, stable, telegramAPI } from "./telegram-release.mjs";

const sha = v => createHash("sha256").update(v).digest("hex");
const version = "v2.0.2", repo = "fixture-a/pool", revision = "a".repeat(40), source = "b".repeat(64);
const body = "## 社群摘要\n新增账号模型通信测试。\n- 改进时间筛选。\n\n## 升级提示\n运行 run.sh，保留现有数据。\n\n## 更新详情\n详细说明。";
test("CI logs retain status and public URL without exposing private receipt fields", () => {
  const receipt = { status: "sent", version, message_url: "https://t.me/example/1", chat_id: "-123", bot_id: 456, content_sha256: source };
  assert.deepEqual(notificationOutput(receipt, true), { status: "sent", version, message_url: "https://t.me/example/1" });
  assert.equal(notificationOutput(receipt, false), receipt);
});
function fixture(t) {
  const directory = mkdtempSync(path.join(os.tmpdir(), "cpap-telegram-test-"));
  chmodSync(directory, 0o700);
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const config = { enabled: true, repository: repo, repository_id: 123, chat_id: "-100123456", bot_username: "FixtureReleaseBot", min_version: version, directory };
  const configFile = path.join(directory, "config.json");
  writeFileSync(configFile, JSON.stringify(config), { mode: 0o600 });
  writeFileSync(path.join(directory, "bot-token"), "123456:fixture-token", { mode: 0o600 });
  const manifest = { version: 1, components: {} };
  const descriptor = { schema_version: 1, release_version: version, revision, image_prefix: "ghcr.io/fixture-a", archive_name: `codex-cpa-pool-${version}.tar.gz`, components: {} };
  let env = `CPAP_RELEASE_VERSION=${version}\nCPAP_RELEASE_REVISION=${revision}\nCPAP_RELEASE_ARCHIVE=${descriptor.archive_name}\n`;
  for (const name of ["control", "web", "gateway", "edge"]) {
    manifest.components[name] = { source_sha256: source };
    descriptor.components[name] = { source_sha256: source, image: `ghcr.io/fixture-a/codex-cpa-${name}:sha256-${source}` };
    env += `CPAP_${name.toUpperCase()}_IMAGE=${descriptor.components[name].image}\n`;
  }
  const files = { [descriptor.archive_name]: "fixture archive", [`release-${version}.json`]: JSON.stringify(descriptor), [`release-${version}.env`]: env, "run.sh": "#!/bin/sh\n" };
  files.SHA256SUMS = Object.entries(files).map(([name, bytes]) => `${sha(bytes)}  ${name}\n`).join("");
  const release = { id: 12, tag_name: version, body, draft: false, prerelease: false, published_at: "2026-09-08T00:00:00Z",
    assets: Object.entries(files).map(([name, bytes]) => ({ name, state: "uploaded", size: Buffer.byteLength(bytes), digest: `sha256:${sha(bytes)}` })) };
  const events = [];
  const chat = { id: Number(config.chat_id), type: "supergroup", username: "fixture_group", permissions: { can_send_messages: true } };
  const api = (method, payload) => {
    events.push(method);
    if (method === "getMe") return { ok: true, result: { id: 42, username: config.bot_username, is_bot: true } };
    if (method === "getChat") return { ok: true, result: chat };
    if (method === "getChatMember") return { ok: true, result: { status: "member" } };
    assert.equal(payload.chat_id, config.chat_id);
    return { ok: true, result: { message_id: 99, chat } };
  };
  const run = (cmd, args) => {
    events.push(`${cmd} ${args.slice(0, 2).join(" ")}`);
    if (cmd === "gh") {
      if (args[0] === "release" && args[1] === "view") return JSON.stringify({ body, isPrerelease: false });
      if (args[0] === "release" && args[1] === "download") {
        const dest = args[args.indexOf("--dir") + 1];
        for (const [name, bytes] of Object.entries(files)) writeFileSync(path.join(dest, name), bytes);
        return "";
      }
      if (args[1] === `repos/${repo}`) return JSON.stringify({ id: config.repository_id, private: false });
      if (args[1].includes("releases/tags")) return JSON.stringify(release);
      if (args[1].includes("git/ref/tags")) return JSON.stringify({ object: { type: "commit", sha: revision } });
    }
    if (cmd === "go") { assert.ok(args.includes("verify")); return "archive verified"; }
    if (cmd === "tar") return args.at(-1) === "release-manifest.json" ? JSON.stringify(manifest) : files["run.sh"];
    if (cmd === "docker") {
      const component = /codex-cpa-(\w+):/.exec(args.at(-1))[1];
      return JSON.stringify({ manifest: { digest: `sha256:${source}` }, image: { config: { Labels: {
        "io.codex-cpa.component": component, "io.codex-cpa.component-digest": source, "io.codex-cpa.source-digest": source
      } } } });
    }
    throw new Error(`Unexpected fixture command: ${cmd} ${args}`);
  };
  return { directory, config, configFile, files, release, descriptor, manifest, events, api, run, chat,
    options: { action: "send", repo, version, revision, configFile, run, apiFactory: () => api } };
}

test("only canonical stable published Releases are eligible; prereleases perform no I/O", () => {
  for (const value of ["v2.0.2-rc.1", "v2.0.2-beta", "v2.0.2-alpha.1", "v2.0.2+build", "v02.0.2", "2.0.2", "v2.0", "../v2.0.2"]) {
    assert.equal(stable(value), false);
    assert.equal(notifyRelease({ action: "send", repo, version: value, configFile: "/missing", run: () => assert.fail("must not use network") }).status, "skipped");
  }
  assert.equal(stable(version), true);
  assert.equal(atLeast("v10.0.0", "v2.9.9"), true);
  assert.equal(atLeast("v2.0.1", version), false);
  for (const flag of ["draft", "prerelease"]) assert.equal(eligible({ tag_name: version, draft: false, prerelease: false, published_at: "now", [flag]: true }, version), false);
});

test("CLI runs through physical and symlinked checkout paths", t => {
  const f = fixture(t);
  const script = fileURLToPath(new URL("./telegram-release.mjs", import.meta.url));
  const alias = path.join(f.directory, "publisher");
  symlinkSync(path.dirname(script), alias, "dir");
  for (const entry of [script, path.join(alias, "telegram-release.mjs")]) {
    const output = execFileSync(process.execPath, [entry, "send", "--repo", repo,
      "--version", "v2.0.2-rc.1", "--config", f.configFile], { encoding: "utf8" });
    assert.equal(JSON.parse(output).status, "skipped");
  }
  const imported = execFileSync(process.execPath, ["--input-type=module", "-"], {
    encoding: "utf8", input: `import ${JSON.stringify(new URL("./telegram-release.mjs", import.meta.url).href)}; console.log("imported");`,
  });
  assert.equal(imported.trim(), "imported");
});

test("stable sends only after artifact/image verification and duplicate invocations reuse the receipt", t => {
  const f = fixture(t);
  const first = notifyRelease(f.options), second = notifyRelease(f.options);
  assert.equal(first.status, "sent"); assert.equal(first.message_id, 99); assert.equal(second.reused, true);
  assert.equal(f.events.filter(e => e === "sendMessage").length, 1);
  assert.ok(f.events.indexOf("sendMessage") > f.events.indexOf("docker buildx imagetools"));
  assert.ok(f.events.includes("go run ./cmd/releasectl"));
  assert.equal(readReceipt(receiptPath(f.config, version)).status, "sent");
  assert.equal(existsSync(receiptPath(f.config, version) + ".lock"), false);
});

test("preview, preflight, disabled notifications and old releases do not send", t => {
  const f = fixture(t);
  assert.equal(notifyRelease({ ...f.options, action: "check" }).status, "ready");
  assert.equal(notifyRelease({ ...f.options, action: "preview" }).status, "preview");
  assert.equal(notifyRelease({ ...f.options, version: "v2.0.1" }).status, "skipped");
  assert.equal(notifyRelease({ ...f.options, action: "status" }).status, "not_sent");
  writeFileSync(f.configFile, JSON.stringify({ ...f.config, enabled: false }));
  assert.equal(notifyRelease(f.options).status, "disabled");
  assert.ok(!f.events.includes("sendMessage"));
});

for (const [name, mutate] of [
  ["Draft", f => { f.release.draft = true; }],
  ["Prerelease flag", f => { f.release.prerelease = true; }],
  ["missing asset", f => { f.release.assets.pop(); }],
  ["unfinished asset", f => { f.release.assets[0].state = "new"; }],
  ["missing notes", f => { f.release.body = "automatic commit log"; }],
  ["checksum mismatch", f => { f.files["run.sh"] = "unexpected content"; }],
  ["archive manifest mismatch", f => { f.manifest.components.control.source_sha256 = "0".repeat(64); }],
  ["revision mismatch", f => { f.options.revision = "0".repeat(40); }],
  ["image mismatch", f => { const run = f.run; f.options.run = (cmd, args) => cmd === "docker" ? '{}' : run(cmd, args); }],
  ["tag-only or missing release", f => { f.options.run = () => { throw new Error("release not found"); }; }]
]) test(`${name} cannot send`, t => {
  const f = fixture(t); mutate(f);
  try { const result = notifyRelease(f.options); assert.equal(result.status, "skipped"); } catch { /* rejected */ }
  assert.ok(!f.events.includes("sendMessage"));
  assert.ok(!existsSync(receiptPath(f.config, version)));
});

test("private configuration fails closed on broad permissions and mismatched repository", t => {
  const f = fixture(t);
  assert.throws(() => readConfig(f.configFile, "other/pool"), /不匹配/);
  chmodSync(f.configFile, 0o644);
  assert.throws(() => readConfig(f.configFile, repo), /0600/);
});

test("destination check accepts a writable member and rejects read-only groups or wrong bots", t => {
  const f = fixture(t);
  assert.equal(checkDestination(f.config, f.api).botID, 42);
  f.chat.permissions.can_send_messages = false;
  assert.throws(() => checkDestination(f.config, f.api), /权限/);
  assert.throws(() => checkDestination({ ...f.config, bot_username: "OtherBot" }, f.api), /身份/);
});

function delivery(f, api, extra = {}) {
  return deliver({ config: f.config, release: f.release, message: renderMessage(f.release, repo), destination: { botID: 42, chat: f.chat }, api, ...extra });
}
test("ambiguous timeout persists unknown and cannot be retried automatically", t => {
  const f = fixture(t); let calls = 0;
  assert.throws(() => delivery(f, () => { calls++; throw new Error("fixture token and response are private"); }), /结果不明/);
  assert.equal(readReceipt(receiptPath(f.config, version)).status, "unknown");
  assert.throws(() => delivery(f, () => { calls++; }), /禁止盲目重发/);
  assert.equal(calls, 1);
});

test("crash-pending receipts and concurrent attempts cannot duplicate a message", t => {
  const f = fixture(t);
  let nested = false;
  delivery(f, (method, payload) => {
    assert.throws(() => delivery(f, () => assert.fail("nested send")), /已有进程/); nested = true;
    return f.api(method, payload);
  });
  assert.equal(nested, true);
  const file = receiptPath(f.config, version), receipt = readReceipt(file);
  writeFileSync(file, JSON.stringify({ ...receipt, status: "pending" }));
  assert.throws(() => delivery(f, () => assert.fail("pending send")), /禁止盲目重发/);
});

for (const response of [{}, { ok: false, error_code: 500 }]) test("unconfirmed server responses block automatic retries", t => {
  const f = fixture(t);
  assert.throws(() => delivery(f, () => response), /待人工核对/);
  assert.equal(readReceipt(receiptPath(f.config, version)).status, "unknown");
  assert.throws(() => delivery(f, () => assert.fail("must not resend")), /禁止盲目重发/);
});

test("confirmed rate limiting honors retry_after with bounded retries", t => {
  const f = fixture(t); const waits = []; let attempts = 0;
  const result = delivery(f, (method, payload) => ++attempts < 3 ? { ok: false, error_code: 429, parameters: { retry_after: 2 } } : f.api(method, payload), { sleep: ms => waits.push(ms) });
  assert.deepEqual(waits, [2000, 2000]); assert.equal(result.attempts, 3); assert.equal(result.status, "sent");
});

test("confirmed rejection can be retried, recreation cannot, explicit edits reuse the message ID", t => {
  const f = fixture(t);
  assert.throws(() => delivery(f, () => ({ ok: false, error_code: 403 })), /403/);
  assert.equal(readReceipt(receiptPath(f.config, version)).status, "failed");
  delivery(f, f.api);
  f.release.body = body.replace("新增", "优化");
  const edited = delivery(f, (method, payload) => { assert.equal(method, "editMessageText"); assert.equal(payload.message_id, 99); return f.api(method, payload); }, { action: "edit" });
  assert.equal(edited.message_id, 99);
  f.release.id++;
  assert.throws(() => delivery(f, f.api), /身份不匹配/);
});

test("message rendering uses curated sections, version release and maintained upgrade links", () => {
  const message = renderMessage({ tag_name: version, body }, repo);
  assert.ok(message.text.includes(`CCPA ${version} 正式发布`));
  assert.ok(!message.text.includes("详细说明"));
  assert.equal(message.parse_mode, "HTML");
  assert.ok(message.text.includes(`<b>CCPA ${version} 正式发布</b>`));
  assert.ok(message.text.includes("<b>本次更新</b>"));
  assert.ok(message.text.includes("• 改进时间筛选。"));
  assert.equal(message.reply_markup.inline_keyboard[0][0].url, `https://github.com/${repo}/releases/tag/${version}`);
  assert.equal(message.reply_markup.inline_keyboard[0][1].url, `https://github.com/${repo}/blob/main/docs/upgrade.md`);
  assert.throws(() => renderMessage({ tag_name: version, body: body + "\n## 社群摘要\n重复" }, repo), /唯一/);
  assert.throws(() => renderMessage({ tag_name: version, body: "## 社群摘要\n更新\n## 升级提示" }, repo), /不能为空/);
  assert.throws(() => renderMessage({ tag_name: version, body: body.replace("新增", "x".repeat(4000)) }, repo), /过长/);
});

test("notification emphasis and commands render safely while raw HTML remains literal", () => {
  const notes = "## 社群摘要\n- **账号管理**：测试 <模型> & 状态。\n\n- **通知服务**：修复发送。\n## 升级提示\n运行：\n`./run.sh`\n`<b>literal & **text**</b>`\n<b>raw</b>\n[说明](https://example.com/?a=1&b=2)";
  const { text } = renderMessage({ tag_name: version, body: notes }, repo);
  assert.ok(text.includes("• <b>账号管理</b>：测试 &lt;模型&gt; &amp; 状态。"));
  assert.ok(text.includes("\n\n• <b>通知服务</b>"));
  assert.ok(text.includes("<code>./run.sh</code>"));
  assert.ok(text.includes("<code>&lt;b&gt;literal &amp; **text**&lt;/b&gt;</code>"));
  assert.ok(text.includes("&lt;b&gt;raw&lt;/b&gt;"));
  assert.ok(text.includes("说明 (https://example.com/?a=1&amp;b=2)"));
  assert.ok(!text.includes("<b>raw</b>"));
});

test("Telegram credentials stay on stdin and subprocess errors never expose them", () => {
  const token = "123456:fixture-token";
  const api = telegramAPI(token, (cmd, args, opts) => {
    assert.equal(cmd, "curl"); assert.ok(!args.join(" ").includes(token));
    assert.ok(opts.input.includes(token)); assert.ok(opts.input.includes('max-time = 25'));
    return JSON.stringify({ ok: true });
  });
  assert.deepEqual(api("getMe", {}), { ok: true });
  const failed = telegramAPI(token, () => { throw new Error(token); });
  assert.throws(() => failed("sendMessage", {}), error => !error.message.includes(token));
});
