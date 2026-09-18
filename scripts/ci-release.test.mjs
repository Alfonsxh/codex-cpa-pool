import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { verifyCIGates } from "./ci-release-gate.mjs";
import { prepareNotification } from "./ci-release-notification.mjs";
import { receiptPath } from "./telegram-release.mjs";

const revision = "a".repeat(40);
const env = { GITHUB_ACTIONS: "true", GITHUB_REPOSITORY: "Alfonsxh/codex-cpa-pool", GITHUB_SHA: revision,
  GITHUB_REF: "refs/heads/main", GITHUB_RUN_ID: "123", GITHUB_RUN_ATTEMPT: "2" };
const run = { head_sha: revision, head_branch: "main", event: "workflow_dispatch", path: ".github/workflows/release.yml", run_attempt: 2 };
const jobs = ["checks / validate", "checks / browser", "checks / package"].map(name => ({ name, status: "completed", conclusion: "success" }));
const read = (current = run, results = jobs) => (endpoint, paginate) => {
  if (paginate) { assert.match(endpoint, /\/attempts\/2\/jobs\?per_page=100$/); return [{ jobs: results.slice(0, 1) }, { jobs: results.slice(1) }]; }
  return current;
};
test("publication requires successful source, browser and package in this run attempt", () => {
  assert.equal(verifyCIGates({ env, revision, read: read() }).attempt, 2);
  for (let i = 0; i < jobs.length; i++) {
    for (const conclusion of ["failure", "cancelled", "skipped", null]) {
      const changed = jobs.map((j, n) => n === i ? { ...j, conclusion } : j);
      assert.throws(() => verifyCIGates({ env, revision, read: read(run, changed) }), /缺少成功验收/);
    }
  }
  assert.throws(() => verifyCIGates({ env, revision, read: read(run, jobs.slice(1)) }));
});
test("reject workstation, wrong workflow, stale attempt and mismatched revision", () => {
  for (const patch of [{ GITHUB_ACTIONS: "false" }, { GITHUB_REF: "refs/heads/topic" }, { GITHUB_SHA: "b".repeat(40) }]) {
    assert.throws(() => verifyCIGates({ env: { ...env, ...patch }, revision, read: read() }));
  }
  for (const patch of [{ run_attempt: 1 }, { head_sha: "b".repeat(40) }, { path: ".github/workflows/ci.yml" }, { event: "pull_request" }]) {
    assert.throws(() => verifyCIGates({ env, revision, read: read({ ...run, ...patch }) }));
  }
});

const config = { enabled: true, repository: "fixture/pool", repository_id: 12, chat_id: "-123", bot_username: "FixtureBot", min_version: "v1.0.0" };
const record = { schema: 1, repository: config.repository, repository_id: 12, chat_id: "-123", version: "v1.0.0", status: "sent", release_id: 13, bot_id: 14 };
function fixture(t) {
  const directory = mkdtempSync(path.join(os.tmpdir(), "cpap-ci-notify-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  return { directory, repo: config.repository, configJSON: JSON.stringify(config), token: "123:fixture" };
}
test("migrate historical receipts once and retain newer Runner state", t => {
  const input = fixture(t);
  const prepared = prepareNotification({ ...input, seedJSON: JSON.stringify([record]) });
  assert.equal(prepared.imported, 1);
  const file = receiptPath(prepared.config, record.version);
  const newer = { ...record, status: "unknown" };
  writeFileSync(file, JSON.stringify(newer));
  assert.equal(prepareNotification({ ...input, seedJSON: JSON.stringify([record]) }).imported, 0);
  assert.deepEqual(JSON.parse(readFileSync(file)), newer);
});
test("refuse missing secrets, changed destinations, unsafe paths and unrelated receipts", t => {
  const input = fixture(t);
  assert.throws(() => prepareNotification({ ...input, token: "" }));
  const prepared = prepareNotification(input);
  assert.throws(() => prepareNotification({ ...input, configJSON: JSON.stringify({ ...config, chat_id: "-456" }) }));
  assert.throws(() => prepareNotification({ ...input, seedJSON: JSON.stringify([{ ...record, repository_id: 99 }]) }));
  const tokenPath = path.join(input.directory, "bot-token");
  rmSync(tokenPath); symlinkSync(prepared.configFile, tokenPath);
  assert.throws(() => prepareNotification(input));
});
