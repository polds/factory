import { promises as fs } from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

const evidence = process.env.M6_EVIDENCE ?? "/evidence";
const signalDir = process.env.M6_SIGNAL ?? "/signal";
const apiKey = process.env.M6_API_KEY ?? "";
const mode = process.env.M6_MODE ?? "offline";
const offline = mode === "offline";
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
  await page.screenshot({ path: path.join(evidence, `${name}.png`), fullPage: true });
}

async function axe(page: Page, name: string): Promise<void> {
  const results = await new AxeBuilder({ page }).analyze();
  const bad = results.violations.filter((row) => row.impact === "serious" || row.impact === "critical");
  for (const row of bad) {
    axeFindings.push(`${name}: ${row.id} ${row.impact}`);
  }
}

async function signIn(page: Page, start = "/", shot = "dashboard"): Promise<void> {
  await page.goto(start);
  await expect(page.getByRole("heading", { name: "Factory", exact: true })).toBeVisible();
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
  await expect(nav.getByRole("button", { name, exact: true })).toBeVisible({ timeout: 90_000 });
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

async function waitForFile(name: string): Promise<void> {
  const target = path.join(signalDir, name);
  const started = Date.now();
  for (;;) {
    try {
      await fs.access(target);
      return;
    } catch {
      if (Date.now() - started > 12 * 60 * 1000) {
        throw new Error(`timed out waiting for ${name}`);
      }
      await delay(1000);
    }
  }
}

async function reloadUntil(page: Page, ready: () => Promise<boolean>, timeoutMs: number): Promise<void> {
  const started = Date.now();
  for (;;) {
    if (await ready()) return;
    if (Date.now() - started > timeoutMs) {
      throw new Error("timed out waiting for the page");
    }
    await page.reload();
    await delay(2000);
  }
}

test("m6 exit loop", async ({ browser }) => {
  if (apiKey === "") throw new Error("M6_API_KEY is empty");
  const context = await browser.newContext({
    baseURL: process.env.FACTORY_URL ?? "http://factoryd-m6:8080",
    recordHar: { path: path.join(evidence, "loop.har"), mode: "full", content: "embed" },
  });
  await context.tracing.start({ screenshots: true, snapshots: true });
  const page = await context.newPage();
  const trace: string[] = ["mode: " + mode];
  try {
    await signIn(page);
    await chooseFactory(page, "m6");
    await createItem(page, "cite one", "Describe the change in one sentence.");
    await createItem(page, "cite two", "Describe the change in one sentence.");
    await createItem(page, "cite three", "Describe the change in one sentence.");
    if (offline) {
      await createItem(page, "m6-bad-label", "m6-bad-label");
    }

    await openSection(page, "Scorers");
    await page.getByRole("link", { name: "Results for cite-sources" }).click();
    await expect(page.getByRole("heading", { name: "Scorers" })).toBeVisible();
    await reloadUntil(
      page,
      async () => (await page.getByRole("row").filter({ hasText: "missed" }).count()) >= 3,
      12 * 60 * 1000,
    );
    const missed = page.getByRole("row").filter({ hasText: "missed" });
    trace.push("scorer: cite-sources");
    const missedIds = await missed.locator("td").first().allTextContents();
    trace.push("failing results: " + missedIds.join(" "));
    if (offline) {
      await expect(page.getByText("Scorer invalid").first()).toBeVisible({ timeout: 180_000 });
      trace.push("invalid label: not-a-label");
    } else {
      const labels = await page.getByRole("cell").allTextContents();
      const declared = labels.some((text) => text.trim() === "followed" || text.trim() === "missed");
      expect(declared).toBe(true);
    }
    await axe(page, "scorers");
    await saveDom(page, "scorers");
    await saveShot(page, "scorers");

    await openSection(page, "Inbox");
    await reloadUntil(
      page,
      async () => (await page.locator('[data-kind="improvement_pr"]').count()) >= 1,
      12 * 60 * 1000,
    );
    const inboxRow = page.locator("li.inbox-row").filter({ has: page.locator('[data-kind="improvement_pr"]') });
    await inboxRow.getByRole("button").first().click();
    const review = inboxRow.getByRole("link", { name: "Review PR" });
    await expect(review).toBeVisible();
    const prHref = await review.getAttribute("href");
    expect(prHref ?? "").toMatch(/^https:/);
    trace.push("pr: " + (prHref ?? ""));
    await axe(page, "inbox");
    await saveDom(page, "inbox");
    await saveShot(page, "inbox");

    await openSection(page, "Improvement");
    await expect(page.getByRole("heading", { name: "Improvement" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Batches" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Regressions addressed" })).toBeVisible();
    await expect(page.getByText("PR open").first()).toBeVisible();
    const batchId = (await page.locator("td.mono").first().textContent()) ?? "";
    trace.push("batch: " + batchId.trim());
    await axe(page, "improvement");
    await saveDom(page, "improvement");
    await saveShot(page, "improvement");

    await waitForFile("candidate-ready");
    const diff = await fs.readFile(path.join(evidence, "skill.diff"), "utf8");
    const paths = [...diff.matchAll(/^diff --git a\/(\S+)/gm)].map((row) => row[1] ?? "");
    expect(paths.length).toBeGreaterThan(0);
    for (const changed of paths) {
      expect(changed.startsWith("skills/")).toBe(true);
    }
    trace.push("skill: " + paths.join(" "));
    if (offline) {
      expect(diff).toContain("CONVENTION: cite-sources");
      const forge = JSON.parse(await fs.readFile(path.join(evidence, "forge.json"), "utf8")) as {
        pulls?: { reviewers?: string[]; creates?: { body?: string; status?: number }[] }[];
        checks?: { summary?: string }[];
      };
      const bodies = (forge.pulls ?? []).flatMap((pull) => pull.creates ?? []).map((row) => row.body ?? "");
      const product = bodies.find((body) => body.includes("## Regressions addressed"));
      expect(product ?? "").toContain("Layer changed: skill");
      expect((product ?? "").split("\n").filter((line) => line.startsWith("- run ")).length).toBeGreaterThanOrEqual(3);
      expect((forge.pulls ?? [])[0]?.reviewers ?? []).toContain("m6-reviewer");
      const guard = (forge.checks ?? []).some((row) => (row.summary ?? "").includes("factory/config"));
      expect(guard).toBe(true);
      trace.push("guard: factory/config passed");
    }

    // GET .../trials is 404 until a benchmark run exists, and the suite page
    // then replaces itself with an error, which hides Run benchmark. Serve an
    // empty matrix until the run is clicked. route.fetch() during reload
    // never returns, so later requests are continued instead. no-store keeps
    // the stand-in out of the cache.
    let trialsLive = false;
    await page.route("**/benchmarks/*/trials*", async (route) => {
      if (trialsLive) {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { "cache-control": "no-store" },
        body: JSON.stringify({ cells: [], report: { winner: "", reason: "" } }),
      });
    });

    await openSection(page, "Benchmarks");
    await page.getByRole("link", { name: "convention", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Benchmarks" })).toBeVisible({ timeout: 60_000 });
    await expect(page.locator("#benchmark-repetitions")).toHaveValue("5");
    await page.getByRole("button", { name: "Run benchmark" }).click();
    const confirm = page.getByRole("dialog", { name: "Run benchmark?" });
    await confirm.getByRole("button", { name: "Run benchmark" }).click();
    await expect(page.getByText(/Benchmark run .+ started/)).toBeVisible();
    const started = (await page.getByText(/Benchmark run .+ started/).textContent()) ?? "";
    trace.push("benchmark run: " + started.replace("Benchmark run ", "").replace(" started.", ""));
    trialsLive = true;
    await reloadUntil(
      page,
      async () => {
        const matrix = page.getByRole("table", { name: "Trial matrix" });
        if ((await matrix.count()) === 0) return false;
        const text = await matrix.innerText();
        if (offline) {
          return text.includes("Winner") && text.includes("100%") && text.includes("0%");
        }
        return text.includes("Winner");
      },
      15 * 60 * 1000,
    );
    const matrixText = await page.getByRole("table", { name: "Trial matrix" }).innerText();
    trace.push("matrix: " + matrixText.replace(/\s+/g, " "));
    if (offline) {
      await expect(page.locator(".callout")).toHaveCount(0);
      expect(matrixText).toContain("100%");
      expect(matrixText).toContain("0%");
      trace.push("d5: Candidate");
    } else {
      await expect(page.getByText("Winner").first()).toBeVisible();
      trace.push("d5: winner present");
    }
    await axe(page, "benchmarks");
    await saveDom(page, "benchmarks");
    await saveShot(page, "benchmarks");

    if (offline) {
      trialsLive = false;
      await openSection(page, "Benchmarks");
      await page.getByRole("link", { name: "fault", exact: true }).click();
      await page.locator("#benchmark-repetitions").fill("1");
      await page.getByRole("button", { name: "Run benchmark" }).click();
      await page.getByRole("dialog", { name: "Run benchmark?" }).getByRole("button", { name: "Run benchmark" }).click();
      trialsLive = true;
      await reloadUntil(
        page,
        async () => (await page.getByText("Infrastructure").count()) >= 1,
        8 * 60 * 1000,
      );
      trace.push("fault errored: infrastructure");
    }

    await openSection(page, "Scorers");
    await page.getByRole("link", { name: "Results for cite-sources" }).click();
    const runLink = page.getByRole("row").filter({ hasText: "missed" }).getByRole("link").first();
    await runLink.click();
    await expect(page.getByRole("heading", { name: "Scores" })).toBeVisible();
    const scoreRow = page.getByRole("row").filter({ has: page.getByRole("rowheader", { name: "cite-sources" }) });
    const before = ((await scoreRow.locator("td").last().textContent()) ?? "").trim();
    await page.getByLabel("Scorer").selectOption("cite-sources");
    await page.getByRole("button", { name: "Score now" }).click();
    const scoreDialog = page.getByRole("dialog", { name: "Score this run?" });
    await scoreDialog.getByRole("button", { name: "Score now" }).click();
    await reloadUntil(
      page,
      async () => {
        const row = page.getByRole("row").filter({ has: page.getByRole("rowheader", { name: "cite-sources" }) });
        if ((await row.count()) === 0) return false;
        const text = (await row.innerText()) ?? "";
        if (text.includes("Pending")) return false;
        const reasoning = ((await row.locator("td").last().textContent()) ?? "").trim();
        if (offline) return /^r\d+$/.test(reasoning) && reasoning !== before;
        return text.includes("followed") || text.includes("missed");
      },
      8 * 60 * 1000,
    );
    await expect(page.getByRole("log", { name: "Run transcript" })).toBeVisible();
    trace.push("score now: left pending");

    await page.goto("/");
    await chooseFactory(page, "m6");
    await expect(page.getByRole("heading", { name: "m6 · scorer cards" })).toBeVisible({ timeout: 60_000 });
    await expect(page.getByRole("heading", { name: "m6 · improvement PRs" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Review PR" }).first()).toBeVisible();
    await axe(page, "dashboard-metrics");
    await saveDom(page, "dashboard-metrics");

    if (axeFindings.length > 0) {
      throw new Error(axeFindings.join("\n"));
    }
    await fs.writeFile(path.join(evidence, "exit-trace.txt"), trace.join("\n") + "\n");
  } finally {
    await context.tracing.stop({ path: path.join(evidence, "loop-trace.zip") }).catch(() => undefined);
    await context.close().catch(() => undefined);
  }
});
