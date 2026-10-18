import { promises as fs } from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Locator, type Page } from "@playwright/test";

const evidence = process.env.M5_EVIDENCE ?? "/evidence";
const signalDir = process.env.M5_SIGNAL ?? "/signal";
const gitURL = process.env.FACTORY_GIT_URL ?? "http://factoryd-git:8080";
const apiKey = process.env.M5_API_KEY ?? "";
const prURL = "https://github.com/polds/m5/pull/7";

let replayPosts = 0;
const axeFindings: string[] = [];

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function saveDom(page: Page, name: string): Promise<void> {
  await fs.writeFile(path.join(evidence, `dom-${name}.html`), await page.content());
}

async function saveShot(page: Page, name: string): Promise<void> {
  await page.screenshot({ path: path.join(evidence, `shot-${name}.png`), fullPage: true });
}

async function axe(page: Page, name: string): Promise<void> {
  const results = await new AxeBuilder({ page }).analyze();
  const bad = results.violations.filter(
    (row) => row.impact === "serious" || row.impact === "critical",
  );
  for (const row of bad) {
    const targets = row.nodes
      .slice(0, 3)
      .map((node) => node.target.join(" "))
      .join("; ");
    axeFindings.push(`${name}: ${row.impact ?? "unknown"} ${row.id}: ${row.help} (${targets})`);
  }
}

function assertAxe(): void {
  if (axeFindings.length > 0) {
    throw new Error(`axe\n${axeFindings.join("\n")}`);
  }
}

async function signal(name: string, body: string): Promise<string> {
  const target = path.join(signalDir, name);
  const ack = path.join(signalDir, `${name}.ack`);
  await fs.rm(ack, { force: true });
  await fs.writeFile(`${target}.tmp`, body);
  await fs.rename(`${target}.tmp`, target);
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      return (await fs.readFile(ack, "utf8")).trim();
    } catch {
      await delay(200);
    }
  }
  throw new Error(`driver did not ack ${name}`);
}

async function signIn(page: Page, start = "/", shot = "dashboard"): Promise<void> {
  await page.goto(start);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await axe(page, `login-${shot}`);
  await saveDom(page, `login-${shot}`);
  await page.getByLabel("API key").fill(apiKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await expect(page.getByText("All factories")).toBeVisible();
  await axe(page, shot);
  await saveShot(page, shot);
}

async function rowIndex(page: Page, title: string): Promise<number> {
  const titles = await page.locator("tr[data-run] a.run-link").allTextContents();
  return titles.findIndex((text) => text.includes(title));
}

async function openSection(page: Page, name: string): Promise<void> {
  await page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name, exact: true }).click();
}

async function chooseFactory(page: Page, name: string): Promise<void> {
  const nav = page.getByRole("navigation", { name: "Factories" });
  await expect(nav.getByRole("button", { name, exact: true })).toBeVisible({ timeout: 60_000 });
  await nav.getByRole("button", { name, exact: true }).click();
}

async function createItem(page: Page, title: string, body: string): Promise<void> {
  await openSection(page, "Work items");
  await page.getByRole("button", { name: "New work item" }).click();
  const dialog = page.getByRole("dialog", { name: "New work item" });
  await dialog.getByLabel("Title").fill(title);
  await dialog.getByLabel("Body").fill(body);
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("link", { name: title }).first()).toBeVisible();
}

async function openRunningRun(page: Page, title: string): Promise<string> {
  await openSection(page, "Runs");
  const row = page
    .locator("tr[data-run]")
    .filter({ hasText: title })
    .filter({ hasText: "Running" })
    .filter({ hasText: "building" });
  await expect(row.first()).toBeVisible({ timeout: 180_000 });
  const runId = (await row.first().getAttribute("data-run")) ?? "";
  if (runId === "") throw new Error(`no run id for ${title}`);
  await row.first().getByRole("link", { name: title }).click();
  return runId;
}

async function killWhenLive(page: Page, title: string): Promise<void> {
  const runId = await openRunningRun(page, title);
  const log = page.getByRole("log", { name: "Run transcript" });
  await expect(log.locator("[data-seq]").first()).toBeVisible({ timeout: 60_000 });
  let ack = "";
  for (let attempt = 0; attempt < 15; attempt += 1) {
    ack = await signal("kill", `kill ${runId}\n`);
    if (ack.startsWith("killed ")) return;
    await delay(1000);
  }
  throw new Error(`step 8: task container was not killed (${ack})`);
}

async function seqs(page: Page): Promise<string[]> {
  return page.locator("[data-seq]").evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-seq") ?? ""),
  );
}

// Newest run row for this title, from the Runs page. The stage is the
// last segment of .run-sub (" · building"). The model cell can contain
// "m5-building" on the offline router and must not count.
type NewestRun = {
  id: string;
  stage: string;
  status: string;
  detail: string;
};

const pastSpecGate = new Set(["building", "reviewing", "human_handoff"]);

function isRunList(url: string): boolean {
  const path = url.split("?")[0] ?? "";
  return path.endsWith("/api/v1/agent/runs");
}

function isInbox(url: string): boolean {
  const path = url.split("?")[0] ?? "";
  return /\/api\/v1\/factory\/[^/]+\/inbox$/.test(path);
}

// isVisible() does not wait. After a section click the inbox is still
// "Loading inbox" until its API response is rendered, so a check in that
// gap misses a row that is about to appear. Same spec in every mode.
async function openInbox(page: Page): Promise<void> {
  const path = new URL(page.url()).pathname;
  if (!path.endsWith("/inbox")) {
    const listed = page.waitForResponse((res) => isInbox(res.url()) && res.ok(), {
      timeout: 15_000,
    });
    await openSection(page, "Inbox");
    await listed;
  } else {
    await openSection(page, "Inbox");
  }
  await expect(page.getByText("Loading inbox", { exact: true })).toBeHidden({ timeout: 15_000 });
  if ((await page.getByRole("heading", { name: "Could not load the inbox" }).count()) > 0) {
    throw new Error("inbox failed to load");
  }
  await expect(
    page
      .locator("ul.inbox-list")
      .or(page.getByRole("heading", { name: "Nothing needs you right now" })),
  ).toBeVisible({ timeout: 15_000 });
}

async function visibleWithin(target: Locator, timeout: number): Promise<boolean> {
  try {
    await target.waitFor({ state: "visible", timeout });
    return true;
  } catch (err) {
    if (err instanceof Error && err.name === "TimeoutError") return false;
    throw err;
  }
}

function stageFromSub(sub: string): string {
  const mark = " · ";
  const at = sub.lastIndexOf(mark);
  if (at < 0) return "";
  return sub.slice(at + mark.length).trim();
}

async function readNewestRun(page: Page, title: string): Promise<NewestRun | null> {
  await openSection(page, "Runs");
  const listed = page.waitForResponse((res) => isRunList(res.url()) && res.ok(), { timeout: 15_000 });
  await page.reload();
  await listed;
  const row = page.locator("tr[data-run]").filter({ hasText: title }).first();
  const visible = await row
    .waitFor({ state: "visible", timeout: 5_000 })
    .then(() => true)
    .catch(() => false);
  if (!visible) return null;
  const id = (await row.getAttribute("data-run")) ?? "";
  const sub = ((await row.locator(".run-sub").textContent()) ?? "").trim();
  const status = (await row.locator("[data-status]").getAttribute("data-status")) ?? "";
  const code = row.locator(".failure-cell .mono");
  const detail = (await code.count()) > 0 ? ((await code.first().textContent()) ?? "").trim() : "";
  return { id, stage: stageFromSub(sub), status, detail };
}

function failedRunError(title: string, run: NewestRun): Error {
  return new Error(`${title}: run ${run.id} ${run.stage} failed (${run.detail || "unknown"})`);
}

// A failed run at any stage, or a run already past the spec gate, is reported
// on the poll that sees it. Triage and planning failures do not wait 180s.
function runBlocksSpecGate(title: string, run: NewestRun | null): Error | null {
  if (!run) return null;
  if (run.status === "failed") return failedRunError(title, run);
  if (pastSpecGate.has(run.stage)) return new Error(`spec gate skipped for ${title}`);
  return null;
}

function specInboxRow(page: Page, title: string) {
  return page
    .locator("li.inbox-row")
    .filter({ hasText: title })
    .filter({ hasText: "spec" })
    .filter({ hasNotText: "Approved by you" });
}

async function approveSpecRow(page: Page, title: string): Promise<void> {
  const row = specInboxRow(page, title);
  await row.getByRole("button").first().click();
  await row.getByRole("button", { name: "Approve" }).click();
  await expect(
    page.locator("li.inbox-row").filter({ hasText: title }).getByText("Approved by you").first(),
  ).toBeVisible();
}

async function approveOnly(page: Page, title: string): Promise<void> {
  await openInbox(page);
  await axe(page, "inbox");
  // Gate detail is "spec". A failed run of the same title has no Approve button.
  const row = specInboxRow(page, title);
  const deadline = Date.now() + 180_000;
  while (Date.now() < deadline) {
    const blocked = runBlocksSpecGate(title, await readNewestRun(page, title));
    await openInbox(page);
    if (blocked) throw blocked;
    if (await visibleWithin(row, 1_000)) {
      await approveSpecRow(page, title);
      return;
    }
    await delay(1000);
  }
  await expect(row).toBeVisible({ timeout: 1_000 });
}

// Same path in offline, live, and live-ci. Success is an agent_reported
// failure at triage, planning, or building. A spec row is not required.
// When one is already open, approve it so the offline building stub can
// still report the failure.
function agentReportedInboxRow(page: Page, runId: string) {
  return page
    .locator("li.inbox-row")
    .filter({ has: page.locator(`a[href$="/runs/${runId}"]`) })
    .filter({ hasText: "agent_reported" })
    .filter({ has: page.locator('[data-kind="failed_run"]') });
}

async function waitAgentReported(page: Page, title: string): Promise<boolean> {
  const deadline = Date.now() + 180_000;
  let approved = false;
  while (Date.now() < deadline) {
    const run = await readNewestRun(page, title);
    if (run?.status === "failed" && run.detail === "agent_reported") {
      if (run.id === "") throw new Error(`${title}: agent_reported run has no id`);
      await openInbox(page);
      const budget = Math.min(15_000, Math.max(1_000, deadline - Date.now()));
      if (await visibleWithin(agentReportedInboxRow(page, run.id), budget)) {
        await openSection(page, "Runs");
        const listed = page.waitForResponse((res) => isRunList(res.url()) && res.ok(), {
          timeout: 15_000,
        });
        await page.reload();
        await listed;
        const match = page.locator(`tr[data-run="${run.id}"]`).filter({ hasText: "agent_reported" });
        if (await visibleWithin(match, budget)) return true;
      }
    } else if (run?.status === "failed") {
      throw failedRunError(title, run);
    } else if (!approved && run && pastSpecGate.has(run.stage)) {
      throw new Error(`spec gate skipped for ${title}`);
    }
    await openInbox(page);
    if (!approved && (await visibleWithin(specInboxRow(page, title), 1_000))) {
      await approveSpecRow(page, title);
      approved = true;
    }
    await delay(1000);
  }
  return false;
}

// Two attempts, then a step 8 failure. approveOnly fail-fasts when a run
// has already failed or moved past the spec gate.
async function driveInfraKill(page: Page): Promise<void> {
  const title = "m5-infra-kill wait for the container to stop";
  const body = "Stay in the model call until the container stops.";
  for (let attempt = 1; attempt <= 2; attempt += 1) {
    await createItem(page, title, body);
    await approveOnly(page, title);
    try {
      await killWhenLive(page, title);
      return;
    } catch {
      // The building call was not killed. One more item, then step 8.
    }
  }
  throw new Error("step 8: infrastructure kill did not land after two attempts");
}

async function driveAgentFailure(page: Page): Promise<void> {
  // The stem stays stable for the offline router and the live prompt.
  // The attempt suffix keeps the oldest-first inbox from mixing the two runs.
  const stem = "m5-agent-failure the agent should report failure";
  const body = "Report the failure and stop.";
  for (let attempt = 1; attempt <= 2; attempt += 1) {
    const title = `${stem} #${attempt}`;
    await createItem(page, title, body);
    if (await waitAgentReported(page, title)) return;
  }
  throw new Error("step 8: agent failure was not agent / agent_reported after two attempts");
}

// Steer comes from the run snapshot, and the harness emits run.capabilities
// only after its first steer long-poll. Reload until that snapshot enables
// the button, while the building model call is still open.
async function waitUntilInterruptEnabled(page: Page): Promise<void> {
  const interrupt = page.locator("#interrupt-run");
  const deadline = Date.now() + 42_000;
  while (Date.now() < deadline) {
    const enabled = await interrupt.isEnabled({ timeout: 10_000 }).catch(() => false);
    if (enabled) return;
    await page.reload();
    await expect(page.getByRole("log", { name: "Run transcript" }).locator("[data-seq]").first()).toBeVisible();
  }
  const reason = ((await page.locator("#steer-reason").textContent().catch(() => "")) ?? "").trim();
  throw new Error(`step 5: interrupt stayed disabled (${reason || "no reason"})`);
}

test("day of factory", async ({ browser }) => {
  if (apiKey === "") throw new Error("M5_API_KEY is empty");
  const context = await browser.newContext({
    baseURL: process.env.FACTORY_URL ?? "http://factoryd:8080",
    recordHar: { path: path.join(evidence, "day.har"), mode: "full", content: "embed" },
  });
  await context.tracing.start({ screenshots: true, snapshots: true });
  const page = await context.newPage();
  const runListURLs: string[] = [];
  page.on("request", (req) => {
    const target = req.url();
    if (req.method() === "POST" && target.includes(":replay")) replayPosts += 1;
    if (req.method() === "GET" && target.includes("/api/v1/agent/runs?")) runListURLs.push(target);
  });
  try {
    await signIn(page);
    await chooseFactory(page, "m5");

    await driveInfraKill(page);
    await driveAgentFailure(page);
    await createItem(page, "m5-scratch", "Scratch item. Leave it at the spec gate.");
    await saveShot(page, "board");
    await axe(page, "work-items");
    await createItem(page, "m5-question", "Need a staging decision.");

    const issueAck = await signal("deliver", '{"op":"issue"}\n');
    if (!issueAck.startsWith("status 2")) throw new Error(`step 6: issue delivery ${issueAck}`);
    const boardRuns = runListURLs.filter((url) => url.includes("factory_id=fac_")).length;
    if (boardRuns === 0) throw new Error("board runs were fetched without factory_id");

    await approveOnly(page, "m5-day");
    const inboxRuns = runListURLs.filter((url) => url.includes("factory_id=fac_")).length;
    if (inboxRuns <= boardRuns) throw new Error("inbox runs were fetched without factory_id");

    await openSection(page, "Runs");
    const building = page
      .locator("tr[data-run]")
      .filter({ hasText: "m5-day" })
      .filter({ hasText: "building" })
      .filter({ hasText: "Running" });
    await expect(building.first()).toBeVisible({ timeout: 180_000 });
    const dayRun = (await building.first().getAttribute("data-run")) ?? "";
    await building.first().getByRole("link", { name: "m5-day" }).click();
    const log = page.getByRole("log", { name: "Run transcript" });
    await expect(log.locator("[data-seq]").first()).toBeVisible({ timeout: 60_000 });
    const before = await seqs(page);
    if (new Set(before).size !== before.length) throw new Error("step 4: duplicate seqs");
    await page.reload();
    await expect(log.locator("[data-seq]").first()).toBeVisible();
    const after = await seqs(page);
    expect(after).toEqual(before);
    await saveShot(page, "transcript");

    await waitUntilInterruptEnabled(page);
    const interrupt = page.locator("#interrupt-run");
    await interrupt.click();
    const confirm = page.getByRole("dialog", { name: "Interrupt this turn?" });
    await confirm.getByRole("button", { name: "Interrupt" }).click();
    await expect(log.getByText("Interrupt", { exact: true })).toBeVisible({ timeout: 30_000 });
    const followUp = page.locator("#follow-up");
    await expect(followUp).toBeEnabled({ timeout: 60_000 });
    await followUp.fill("m5-follow-up Reply with a one-line summary and no tool calls.");
    await page.getByRole("button", { name: "Send follow-up" }).click();
    await expect(log.getByText("Follow-up", { exact: true })).toBeVisible({ timeout: 60_000 });
    const steerList = page.locator("ul.steer-list");
    await expect(steerList.getByText("interrupt", { exact: true })).toBeVisible({ timeout: 30_000 });
    await expect(steerList.getByText("followup", { exact: true })).toBeVisible({ timeout: 30_000 });
    await expect(steerList.getByText("delivered", { exact: true })).toHaveCount(2, { timeout: 30_000 });
    const idleAck = await signal("idle", `${dayRun}\n`);
    if (idleAck !== "running") throw new Error(`step 5: container was ${idleAck} during idle`);

    await openSection(page, "Workers");
    await expect(page.getByRole("heading", { name: "Workers" })).toBeVisible();
    const worker = page.getByRole("row", { name: /local-1/ });
    await expect(worker).toBeVisible({ timeout: 60_000 });
    await expect(worker).toContainText("/ 3");
    await expect(worker).not.toContainText("never");
    await expect(page.getByText("Queue depth")).toBeVisible();
    await axe(page, "workers");
    await saveShot(page, "workers");

    await openSection(page, "Secrets");
    await expect(page.getByRole("heading", { name: "Secrets" })).toBeVisible();
    await expect(
      page.getByText("Values are never shown. This page lists names, providers and presence only."),
    ).toBeVisible();
    const secrets = page.getByRole("table", { name: "Secret names and presence" });
    await expect(secrets.getByRole("row", { name: /M5_WEBHOOK/ })).toContainText("Present");
    await expect(secrets.getByRole("row", { name: /M5_PLANTED/ })).toContainText("Present");
    await axe(page, "secrets");
    await saveDom(page, "secrets");
    await saveShot(page, "secrets");

    await openSection(page, "Automations");
    await expect(
      page.getByText("Runtime override: survives definition applies and doesn't edit the definition."),
    ).toBeVisible();
    await axe(page, "automations");
    await page.getByRole("switch", { name: "Pause issue-intake" }).click();
    await page.getByRole("dialog", { name: "Pause issue-intake?" }).getByRole("button", { name: "Pause" }).click();
    await expect(page.getByText("Paused at runtime")).toBeVisible();
    await page.getByRole("switch", { name: "Resume issue-intake" }).click();
    await page.getByRole("dialog", { name: "Resume issue-intake?" }).getByRole("button", { name: "Resume" }).click();
    await expect(page.getByText("Paused at runtime")).toHaveCount(0);

    await openSection(page, "Work items");
    const done = page.getByRole("region", { name: "Done" });
    const dayCard = done.getByRole("link", { name: "m5-day" });
    // The harness emits the terminal event after idleTimeoutMinutes (5), and
    // reviewing starts only then. This wait covers that idle plus the review run.
    await expect(dayCard).toBeVisible({ timeout: 8 * 60 * 1000 });
    await expect(dayCard).toContainText("m5-reviewing");
    await dayCard.click();
    await expect(page.locator("strong").filter({ hasText: /^Handoff$/ })).toBeVisible();
    const prAck = await signal("deliver", '{"op":"pr"}\n');
    if (!prAck.startsWith("status 2")) throw new Error(`step 6: pr delivery ${prAck}`);
    await page.reload();
    await expect(page.locator("strong").filter({ hasText: /^Handoff$/ })).toBeVisible();
    await expect(page.getByRole("link", { name: prURL })).toBeVisible({ timeout: 30_000 });
    await axe(page, "work-item");
    await saveShot(page, "handoff");

    await openSection(page, "Runs");
    await axe(page, "runs");
    await expect(page.getByRole("columnheader", { name: "Started" })).toBeVisible();
    const questionAt = await rowIndex(page, "m5-question");
    const agentAt = await rowIndex(page, "m5-agent-failure");
    if (questionAt < 0 || agentAt < 0 || questionAt > agentAt) {
      throw new Error(`created_at order question ${questionAt} agent ${agentAt}`);
    }
    await expect(
      page
        .locator("tr[data-run]")
        .filter({ hasText: "m5-agent-failure" })
        .first()
        .locator("td")
        .last()
        .locator("time"),
    ).toHaveAttribute("datetime", /^\d{4}-/);
    await page.locator("#filter-source").selectOption("manual");
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-agent-failure" }).first()).toBeVisible();
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-day" })).toHaveCount(0);
    await page.getByRole("button", { name: "Clear filters" }).click();
    await page.locator("#filter-worker").selectOption("local-1");
    await expect(page.locator("tr[data-run]").first()).toBeVisible();
    await page.getByRole("button", { name: "Clear filters" }).click();
    const failureGroup = page.getByRole("group", { name: "Failure class" });
    await failureGroup.getByRole("button", { name: "Agent" }).click();
    const agentListRow = page
      .locator("tr[data-run]")
      .filter({ hasText: "m5-agent-failure" })
      .filter({ hasText: "agent_reported" })
      .first();
    await expect(agentListRow).toBeVisible({ timeout: 60_000 });
    await expect(agentListRow).toContainText("agent_reported");
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-infra-kill" })).toHaveCount(0);
    await agentListRow.getByRole("link").click();
    const agentPanel = page.getByRole("region", { name: "Failure" });
    await expect(agentPanel).toHaveAttribute("data-failure-class", "agent");
    await expect(page.locator(".meta-row > div").filter({ hasText: "Stage" }).locator("dd")).toHaveText(
      /^(triage|planning|building)$/,
    );
    await expect(agentPanel.locator("code", { hasText: "agent_reported" })).toBeVisible();
    await expect(agentPanel.getByText("Agent reported this run as failed")).toBeVisible();
    await expect(agentPanel.getByRole("list", { name: "Evidence" }).getByRole("listitem").first()).toBeVisible();
    await expect(
      page.getByRole("log", { name: "Run transcript" }).getByText(/m5 agent failure|failed_by_agent/).first(),
    ).toBeVisible();
    await saveShot(page, "agent-failure");

    await openSection(page, "Runs");
    await failureGroup.getByRole("button", { name: "Infrastructure" }).click();
    const infraListRow = page
      .locator("tr[data-run]")
      .filter({ hasText: "m5-infra-kill" })
      .filter({ hasText: "container_killed" })
      .first();
    await expect(infraListRow).toBeVisible();
    await expect(infraListRow).toContainText("container_killed");
    await expect(infraListRow).toContainText("exit 137");
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-agent-failure" })).toHaveCount(0);
    await infraListRow.getByRole("link").click();
    const infraPanel = page.getByRole("region", { name: "Failure" });
    await expect(infraPanel).toHaveAttribute("data-failure-class", "infrastructure");
    await expect(page.locator(".meta-row > div").filter({ hasText: "Stage" }).locator("dd")).toHaveText(
      "building",
    );
    await expect(infraPanel.locator("code", { hasText: "container_killed" })).toBeVisible();
    await expect(infraPanel.getByRole("list", { name: "Evidence" }).getByRole("listitem").first()).toBeVisible();
    await expect(page.locator(".meta-row > div").filter({ hasText: "Created" }).locator("time")).toHaveAttribute(
      "datetime",
      /^\d{4}-/,
    );
    await expect(page.locator(".meta-row > div").filter({ hasText: "Started" }).locator("time")).toHaveAttribute(
      "datetime",
      /^\d{4}-/,
    );
    await expect(page.locator(".meta-row > div").filter({ hasText: "Cost" }).locator("dd")).toHaveText(/^(—|\$)/);
    await page.getByText("Raw details").click();
    await expect(
      page.locator(".raw-details dt").filter({ hasText: "exit_code" }).locator("xpath=following-sibling::dd[1]"),
    ).toHaveText("137");
    await axe(page, "run-detail");
    await saveShot(page, "infra-failure");

    await openSection(page, "Definition");
    await chooseFactory(page, "m5");
    const schemaLink = page.getByRole("link", { name: "Schema: v1alpha1" });
    await expect(schemaLink).toBeVisible();
    const schemaHref = await schemaLink.getAttribute("href");
    if (schemaHref === null || !schemaHref.includes("/api/v1/factory-files/schemas/v1alpha1")) {
      throw new Error(`definition schema link href ${schemaHref ?? ""}`);
    }
    const schemaPage = await page.context().newPage();
    try {
      const schemaResponse = await schemaPage.goto(new URL(schemaHref, page.url()).href);
      if (schemaResponse === null) throw new Error("definition schema navigation had no response");
      expect(schemaResponse.status()).toBe(200);
      const schemaType = schemaResponse.headers()["content-type"] ?? "";
      if (!schemaType.includes("json")) throw new Error(`definition schema content-type ${schemaType}`);
      const schemaBody: unknown = JSON.parse(await schemaResponse.text());
      if (
        typeof schemaBody !== "object" ||
        schemaBody === null ||
        !Object.prototype.hasOwnProperty.call(schemaBody, "factory.schema.json")
      ) {
        throw new Error("definition schema body missing factory.schema.json");
      }
    } finally {
      await schemaPage.close();
    }
    await page.getByRole("tree", { name: "Definition files" }).getByRole("treeitem", { name: "factory.yaml" }).click();
    const editor = page.getByRole("textbox", { name: "Contents of factory.yaml" });
    const original = await editor.inputValue();
    await editor.fill(original.replace("schemaVersion: v1alpha1", "schemaVersion: no-such"));
    await page.getByRole("button", { name: "Validate" }).click();
    const diagnostics = page.getByRole("region", { name: /Diagnostics/ });
    await expect(diagnostics.getByRole("button", { name: /factory\.yaml:\d+/ })).toBeVisible();
    await expect(diagnostics.getByText(/unsupported schemaVersion/)).toBeVisible();
    const fixed = original.replace(
      "description: Day of factory.",
      "description: Day of factory. m5-applied-marker",
    );
    await editor.fill(fixed);
    await expect(editor).toHaveValue(/m5-applied-marker/);
    await page.getByRole("button", { name: "Validate" }).click();
    await expect(diagnostics.getByText("0 errors")).toBeVisible({ timeout: 30_000 });
    await expect(diagnostics.getByText(/not an authorization boundary/).first()).toBeVisible();
    await expect(page.getByRole("button", { name: "Apply" })).toBeEnabled();
    await page.getByRole("button", { name: "Apply" }).click();
    const apply = page.getByRole("dialog", { name: "Apply definition" });
    await expect(apply.getByText("factory.yaml")).toBeVisible();
    await apply.getByRole("button", { name: "Confirm apply" }).click();
    await expect(apply).toBeHidden({ timeout: 30_000 });
    await expect(page.getByText("The server applied a different definition than your edits")).toHaveCount(0);
    await page.reload();
    await page.getByRole("tree", { name: "Definition files" }).getByRole("treeitem", { name: "factory.yaml" }).click();
    await expect(page.getByRole("textbox", { name: "Contents of factory.yaml" })).toHaveValue(
      /m5-applied-marker/,
    );
    await axe(page, "definition");
    await saveShot(page, "definition");

    await signIn(page, `${gitURL}/`, "dashboard-git");
    await chooseFactory(page, "m5-git");
    await openSection(page, "Definition");
    await expect(page.getByText(/Read-only\. Changes go through a pull request\./)).toBeVisible({
      timeout: 90_000,
    });
    await expect(page.getByText(/Production ref\s+main\s+@\s+[0-9a-f]{7}/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Validate" })).toHaveCount(0);
    await expect(page.getByRole("textbox", { name: /Contents of/ })).toHaveCount(0);
    await axe(page, "definition-git");
    await saveShot(page, "definition-git");

    await page.goto("/");
    await chooseFactory(page, "m5");
    await openSection(page, "Webhooks");
    await axe(page, "webhooks");
    const ingress = page.getByText(/\/api\/v1\/webhooks\/whk_[0-9A-Z]+/);
    await expect(ingress).toBeVisible();
    const uid = ((await ingress.innerText()).match(/whk_[0-9A-Z]+/) ?? [""])[0];
    if (uid === "") throw new Error("step 10: webhook uid was not on the page");
    const unmatched = await signal(
      "deliver",
      `${JSON.stringify({ op: "webhook", match: false, uid })}\n`,
    );
    if (!unmatched.startsWith("status 2")) throw new Error(`step 10: unmatched ${unmatched}`);
    await page.getByRole("button", { name: "ci", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Recent deliveries" })).toBeVisible();
    await expect(page.getByText(/m5-wh-/).first()).toBeVisible();
    await openSection(page, "Deliveries");
    await axe(page, "deliveries");
    await page.getByRole("tab", { name: /Unmatched/ }).click();
    await page.locator("table").getByRole("button", { name: /^m5-pr-/ }).click();
    const normalized = page.getByRole("region", { name: "Normalized event" });
    await expect(normalized).toBeVisible();
    await expect(normalized).toContainText("github");
    await expect(normalized).toContainText("pull_request_opened");
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const delivery = page.locator("table").getByRole("button", { name: /^m5-wh-/ });
    await expect(delivery).toBeVisible();
    await delivery.click();
    await page.getByRole("button", { name: "Replay delivery" }).click();
    await page.getByRole("dialog", { name: "Replay this delivery?" }).getByRole("button", { name: "Replay" }).click();
    await expect(page.getByText("Replayed just now").first()).toBeVisible();
    expect(replayPosts).toBe(1);
    await saveShot(page, "replay");
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const matched = await signal(
      "deliver",
      `${JSON.stringify({ op: "webhook", match: true, uid })}\n`,
    );
    if (!matched.startsWith("status 2")) throw new Error(`step 10: match ${matched}`);

    await openInbox(page);
    const question = page.locator("li.inbox-row").filter({ hasText: "m5-question" });
    const questionDeadline = Date.now() + 90_000;
    let sawQuestion = false;
    while (Date.now() < questionDeadline) {
      const run = await readNewestRun(page, "m5-question");
      if (run?.status === "failed") throw failedRunError("m5-question", run);
      await openInbox(page);
      await page.getByRole("group", { name: "Filter by kind" }).getByRole("button", { name: "Questions" }).click();
      if (await visibleWithin(question.getByText("Which staging env?"), 1_000)) {
        sawQuestion = true;
        break;
      }
      await delay(1000);
    }
    if (!sawQuestion) {
      await openSection(page, "Runs");
      const rows = page.locator("tr[data-run]").filter({ hasText: "m5-question" });
      await expect(rows.first()).toBeVisible({ timeout: 30_000 });
      const count = await rows.count();
      const chunks: string[] = [];
      for (let i = 0; i < count; i += 1) {
        await openSection(page, "Runs");
        await page
          .locator("tr[data-run]")
          .filter({ hasText: "m5-question" })
          .nth(i)
          .getByRole("link")
          .click();
        const transcript = page.getByRole("log", { name: "Run transcript" });
        await expect(transcript.locator("[data-seq]").first()).toBeVisible({ timeout: 30_000 });
        chunks.push(await transcript.innerText());
      }
      const text = chunks.join("\n");
      if (text.includes("unknown tool") || text.includes("ask_input")) {
        throw new Error("step 7: factory-harness emits no question event (T47c not landed)");
      }
      throw new Error("step 7: question did not reach the inbox");
    }
    await question.getByRole("button").first().click();
    const runHref = await question.locator("a[href*='/runs/']").getAttribute("href");
    await question.getByLabel("Answer").fill("staging-a");
    await question.getByRole("button", { name: "Send answer" }).click();
    await expect(question.getByText("Which staging env?")).toBeHidden({ timeout: 30_000 });
    if (runHref) await page.goto(runHref);
    await expect(
      page.getByRole("log", { name: "Run transcript" }).getByText("staging noted").first(),
    ).toBeVisible({
      timeout: 120_000,
    });
    await saveShot(page, "question");
    assertAxe();
  } finally {
    await fs.writeFile(path.join(signalDir, "done"), "done\n");
    await context.tracing.stop({ path: path.join(evidence, "day-trace.zip") });
    await context.close();
  }
});

test.describe("after T47b", () => {
  test.fixme("git definition shows a View on forge link when forge_url is https", async () => {});
});
