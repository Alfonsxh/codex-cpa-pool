import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { realpathSync } from "node:fs";

// Read GitHub's current run attempt instead of trusting a caller-provided skip flag
// or successful jobs from an earlier attempt/revision.
export function verifyCIGates({ env = process.env, revision, read = (endpoint, paginate = false) => JSON.parse(execFileSync("gh", ["api", endpoint, ...(paginate ? ["--paginate", "--slurp"] : [])], { encoding: "utf8" })) }) {
  const repo = env.GITHUB_REPOSITORY;
  if (env.GITHUB_ACTIONS !== "true" || repo !== "Alfonsxh/codex-cpa-pool" ||
      env.GITHUB_REF !== "refs/heads/main" || env.GITHUB_SHA !== revision ||
      !/^[a-f0-9]{40}$/.test(revision || "") || !/^[1-9][0-9]*$/.test(env.GITHUB_RUN_ID || "") ||
      !/^[1-9][0-9]*$/.test(env.GITHUB_RUN_ATTEMPT || "")) throw new Error("正式发布必须由 main 的 GitHub Release 工作流执行");
  const base = `repos/${repo}/actions/runs/${env.GITHUB_RUN_ID}`;
  const run = read(base);
  if (run.head_sha !== revision || run.head_branch !== "main" || run.event !== "workflow_dispatch" ||
      run.path !== ".github/workflows/release.yml" || run.run_attempt !== Number(env.GITHUB_RUN_ATTEMPT)) throw new Error("发布工作流或提交身份不一致");
  const jobs = read(`${base}/attempts/${env.GITHUB_RUN_ATTEMPT}/jobs?per_page=100`, true).flatMap(page => page.jobs);
  for (const name of ["checks / validate", "checks / browser", "checks / package"]) {
    const matches = jobs.filter(job => job.name === name);
    if (matches.length !== 1 || matches[0].status !== "completed" || matches[0].conclusion !== "success") throw new Error(`当前发布缺少成功验收：${name}`);
  }
  return { revision, run: env.GITHUB_RUN_ID, attempt: Number(env.GITHUB_RUN_ATTEMPT) };
}

if (process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  console.log(JSON.stringify(verifyCIGates({ revision: process.argv[2] })));
}
