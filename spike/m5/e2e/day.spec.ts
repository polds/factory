import { promises as fs } from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

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
  await expect(page.getByRole("link", { name: title })).toBeVisible();
}

async function openRunningRun(page: Page, title: string): Promise<string> {
  await openSection(page, "Runs");
  const row = page
    .locator("tr[data-run]")
    .filter({ hasText: title })
    .filter({ hasText: "Running" });
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

async function approveOnly(page: Page, title: string): Promise<void> {
  await openSection(page, "Inbox");
  await axe(page, "inbox");
  const row = page.locator("li.inbox-row").filter({ hasText: title });
  await expect(row).toBeVisible({ timeout: 180_000 });
  await row.getByRole("button").first().click();
  await row.getByRole("button", { name: "Approve" }).click();
  await expect(row.getByText("Approved by you")).toBeVisible();
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
  page.on("request", (req) => {
    if (req.method() === "POST" && req.url().includes(":replay")) replayPosts += 1;
  });
  try {
    await signIn(page);
    await chooseFactory(page, "m5");

    await createItem(
      page,
      "m5-infra-kill",
      "Stay in the model call until the container stops.",
    );
    await killWhenLive(page, "m5-infra-kill");

    await createItem(
      page,
      "m5-agent-failure",
      "Report the failure and stop.",
    );
    await createItem(page, "m5-scratch", "Scratch item. Leave it at the spec gate.");
    await saveShot(page, "board");
    await axe(page, "work-items");
    await createItem(page, "m5-question", "Need a staging decision.");

    const issueAck = await signal("deliver", '{"op":"issue"}\n');
    if (!issueAck.startsWith("status 2")) throw new Error(`step 6: issue delivery ${issueAck}`);

    await approveOnly(page, "m5-day");

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
    await expect(done.getByRole("link", { name: "m5-day" })).toBeVisible({
      timeout: 240_000,
    });
    await done.getByRole("link", { name: "m5-day" }).click();
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
    const failureGroup = page.getByRole("group", { name: "Failure class" });
    await failureGroup.getByRole("button", { name: "Agent" }).click();
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-agent-failure" })).toBeVisible({
      timeout: 60_000,
    });
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-infra-kill" })).toHaveCount(0);
    await page.locator("tr[data-run]").filter({ hasText: "m5-agent-failure" }).getByRole("link").click();
    const agentPanel = page.getByRole("region", { name: "Failure" });
    await expect(agentPanel).toHaveAttribute("data-failure-class", "agent");
    await expect(agentPanel.locator("code", { hasText: "agent_reported" })).toBeVisible();
    await expect(agentPanel.getByText("Agent reported this run as failed")).toBeVisible();
    await expect(agentPanel.getByRole("list", { name: "Evidence" }).getByRole("listitem").first()).toBeVisible();
    await expect(
      page.getByRole("log", { name: "Run transcript" }).getByText("m5 agent failure").first(),
    ).toBeVisible();
    await saveShot(page, "agent-failure");

    await openSection(page, "Runs");
    await failureGroup.getByRole("button", { name: "Infrastructure" }).click();
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-infra-kill" })).toBeVisible();
    await expect(page.locator("tr[data-run]").filter({ hasText: "m5-agent-failure" })).toHaveCount(0);
    await page.locator("tr[data-run]").filter({ hasText: "m5-infra-kill" }).getByRole("link").click();
    const infraPanel = page.getByRole("region", { name: "Failure" });
    await expect(infraPanel).toHaveAttribute("data-failure-class", "infrastructure");
    await expect(infraPanel.locator("code", { hasText: "container_killed" })).toBeVisible();
    await expect(infraPanel.getByRole("list", { name: "Evidence" }).getByRole("listitem").first()).toBeVisible();
    await axe(page, "run-detail");
    await saveShot(page, "infra-failure");

    await openSection(page, "Definition");
    await chooseFactory(page, "m5");
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
    try {
      await expect(page.getByText("No errors. Ready to apply.")).toBeVisible({ timeout: 30_000 });
    } catch (err) {
      const diag = await page.getByRole("region", { name: /Diagnostics/ }).innerText();
      throw new Error(`step 11: ${diag}`, { cause: err });
    }
    await page.getByRole("button", { name: "Apply" }).click();
    const apply = page.getByRole("dialog", { name: "Apply definition" });
    await expect(apply.getByText("factory.yaml")).toBeVisible();
    await apply.getByRole("button", { name: "Confirm apply" }).click();
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
    await openSection(page, "Deliveries");
    await axe(page, "deliveries");
    await page.getByRole("tab", { name: /Unmatched/ }).click();
    await page.getByRole("row", { name: /\bci\b/ }).click();
    await page.getByRole("button", { name: "Replay delivery" }).click();
    await page.getByRole("dialog", { name: "Replay this delivery?" }).getByRole("button", { name: "Replay" }).click();
    await expect(page.getByText("Replayed just now").first()).toBeVisible();
    expect(replayPosts).toBe(1);
    await saveShot(page, "replay");
    const matched = await signal(
      "deliver",
      `${JSON.stringify({ op: "webhook", match: true, uid })}\n`,
    );
    if (!matched.startsWith("status 2")) throw new Error(`step 10: match ${matched}`);

    await openSection(page, "Inbox");
    await page.getByRole("group", { name: "Filter by kind" }).getByRole("button", { name: "Questions" }).click();
    const question = page.locator("li.inbox-row").filter({ hasText: "m5-question" });
    const sawQuestion = await question
      .getByText("Which staging env?")
      .waitFor({ timeout: 90_000 })
      .then(() => true)
      .catch(() => false);
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
  test.fixme("run list Started column and created_at order", async () => {});
  test.fixme("failure cell shows failure_code and exit", async () => {});
  test.fixme("worker and source filters use real row fields", async () => {});
  test.fixme("run detail shows exit_code, created and started times, and cost_cents", async () => {});
  test.fixme("recent steers list shows the interrupt and follow-up with delivered_at", async () => {});
  test.fixme("board and inbox runs are fetched with factory_id", async () => {});
  test.fixme("board latest run is chosen by created_at", async () => {});
  test.fixme("git definition shows a View on forge link when forge_url is https", async () => {});
  test.fixme("delivery detail shows a Normalized event section", async () => {});
  test.fixme("webhook page shows per-webhook delivery history", async () => {});
});
