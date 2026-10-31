import { promises as fs } from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Locator, type Page } from "@playwright/test";

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
    const targets = row.nodes
      .slice(0, 3)
      .map((node) => node.target.join(" "))
      .join("; ");
    axeFindings.push(`${name}: ${row.id} ${row.impact} (${targets})`);
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

async function metricText(row: Locator, label: string): Promise<string> {
  const dd = row
    .locator("dt")
    .filter({ hasText: new RegExp("^" + label + "$") })
    .locator("xpath=following-sibling::dd[1]");
  if ((await dd.count()) === 0) return "";
  return ((await dd.first().innerText()) ?? "").replace(/\s+/g, " ").trim();
}

function matrixRow(page: Page, configuration: string): Locator {
  return page.getByRole("table", { name: "Trial matrix" }).getByRole("row").filter({
    has: page.getByRole("rowheader", { name: new RegExp(configuration, "i") }),
  });
}

async function conventionReady(page: Page): Promise<boolean> {
  const table = page.getByRole("table", { name: "Trial matrix" });
  if ((await table.count()) === 0) return false;
  const candidate = matrixRow(page, "Candidate");
  const baseline = matrixRow(page, "Baseline");
  if ((await candidate.count()) !== 1 || (await baseline.count()) !== 1) return false;
  const candidateHeader = ((await candidate.getByRole("rowheader").innerText()) ?? "").toLowerCase();
  const baselineHeader = ((await baseline.getByRole("rowheader").innerText()) ?? "").toLowerCase();
  if (!candidateHeader.includes("winner") || baselineHeader.includes("winner")) return false;
  if ((await metricText(candidate, "n")) !== "5" || (await metricText(baseline, "n")) !== "5") return false;
  if ((await metricText(candidate, "Errored")) !== "0" || (await metricText(baseline, "Errored")) !== "0") return false;
  const candidateRate = await metricText(candidate, "Pass rate");
  const baselineRate = await metricText(baseline, "Pass rate");
  if (offline) return candidateRate === "100%" && baselineRate === "0%";
  return /^\d+(?:\.\d+)?%$/.test(candidateRate) && /^\d+(?:\.\d+)?%$/.test(baselineRate);
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

async function failedImprovement(page: Page): Promise<string> {
  const rows = page.locator("li.inbox-row").filter({
    has: page.locator('[data-kind="failed_run"]'),
  });
  const count = await rows.count();
  for (let i = 0; i < count; i += 1) {
    const row = rows.nth(i);
    const text = ((await row.innerText()) ?? "").replace(/\s+/g, " ").trim();
    if (!text.includes("improve:")) continue;
    const runID = ((await row.locator(".mono").last().innerText()) ?? "").trim();
    const detail = ((await row.locator(".muted").first().innerText()) ?? "").trim();
    return `improvement run failed ${runID}: ${detail || text}`;
  }
  const factoryId = new URL(page.url()).pathname.split("/")[2] ?? "";
  if (factoryId === "") return "";
  const response = await page.request.get(
    `/api/v1/factory/${encodeURIComponent(factoryId)}/improvement-batches`,
    {
      headers: { Authorization: `Bearer ${apiKey}` },
      failOnStatusCode: false,
    },
  );
  if (!response.ok()) return "";
  const batches = (await response.json()) as { id?: string; status?: string; agent?: string }[];
  for (const batch of batches) {
    if (batch.status !== "failed") continue;
    return `improvement batch failed ${batch.id ?? ""} agent ${batch.agent ?? ""}`;
  }
  return "";
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
    await createItem(page, scoped("cite one"), "Describe the change in one sentence.");
    await createItem(page, scoped("cite two"), "Describe the change in one sentence.");
    await createItem(page, scoped("cite three"), "Describe the change in one sentence.");
    if (offline) {
      await createItem(page, scoped("m6-bad-label"), "m6-bad-label");
    }
    await axe(page, "work-items");

    await openSection(page, "Scorers");
    await page.getByRole("link", { name: "Results for cite-sources" }).click();
    await expect(page.getByRole("heading", { name: "Scorers" })).toBeVisible();
    await reloadUntil(page, async () => (await missedIds(page)).length >= 3, 12 * 60 * 1000);
    trace.push("scorer: cite-sources");
    const failingIds = await missedIds(page);
    trace.push("failing results: " + failingIds.join(" "));
    if (offline) {
      if (runNonce === "") {
        await expect(page.getByText("Scorer invalid").first()).toBeVisible({ timeout: 180_000 });
      } else {
        const allow = await runsForNonce(page);
        await expect
          .poll(async () => {
            const rows = page.getByRole("row").filter({ hasText: "Scorer invalid" });
            const count = await rows.count();
            for (let i = 0; i < count; i += 1) {
              const id = ((await rows.nth(i).getByRole("link").first().innerText()) ?? "").trim();
              if (allow.has(id)) return true;
            }
            return false;
          })
          .toBe(true);
      }
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
      async () => {
        const failed = await failedImprovement(page);
        if (failed !== "") throw new Error(failed);
        return (await page.locator('[data-kind="improvement_pr"]').count()) >= 1;
      },
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
    const regressions =
      runNonce === ""
        ? page.getByRole("table", { name: /Regressions for/ })
        : page.getByRole("table", { name: `Regressions for ${batchId.trim()}` });
    await expect(regressions).toBeVisible();
    const regressionRows = regressions.locator("tbody").getByRole("row");
    await expect(regressionRows).toHaveCount(failingIds.length);
    for (const id of failingIds) {
      const row = regressionRows.filter({ has: page.getByRole("link", { name: id, exact: true }) });
      await expect(row).toHaveCount(1);
      await expect(row.getByRole("cell", { name: "cite-sources", exact: true })).toBeVisible();
      await expect(row.getByRole("cell", { name: "missed", exact: true })).toBeVisible();
    }
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
        pulls?: { head?: string; reviewers?: string[]; creates?: { body?: string; status?: number }[] }[];
        checks?: { summary?: string }[];
        lookups?: { head?: string }[];
      };
      const pulls = forge.pulls ?? [];
      const creates = pulls.flatMap((pull) => pull.creates ?? []);
      const productBody = (row: { body?: string; status?: number } | undefined): string => row?.body ?? "";
      const linesFor = (body: string): number =>
        body.split("\n").filter((line) => line.startsWith("- run ")).length;
      const guard = (forge.checks ?? []).some((row) => (row.summary ?? "").includes("factory/config"));
      if (process.env.M6_IMPROVER_OPENS_PR === "1") {
        const improver = creates.find(
          (row) => row.status === 201 && (row.body ?? "").includes("Opened by the offline improver."),
        );
        const product = creates.find(
          (row) => row.status === 422 && (row.body ?? "").includes("## Regressions addressed"),
        );
        const body = productBody(product);
        expect(improver?.status).toBe(201);
        expect(product?.status).toBe(422);
        expect(body).toContain("Layer changed: skill");
        expect(linesFor(body)).toBe(failingIds.length);
        for (const id of failingIds) {
          expect(body).toContain("- run " + id + " ");
        }
        const head = "m6:improve/" + batchId.trim();
        expect((forge.lookups ?? []).some((row) => row.head === head)).toBe(true);
        const forHead = pulls.filter((pull) => pull.head === "improve/" + batchId.trim());
        expect(forHead).toHaveLength(1);
        expect(forHead[0]?.reviewers ?? []).toContain("m6-reviewer");
        expect(guard).toBe(true);
      } else {
        const product = creates.find((row) => {
          if (row.status !== 201 || !(row.body ?? "").includes("## Regressions addressed")) return false;
          if (runNonce === "") return true;
          return failingIds.every((id) => (row.body ?? "").includes("- run " + id + " "));
        });
        const body = productBody(product);
        expect(product?.status).toBe(201);
        expect(body).toContain("Layer changed: skill");
        expect(linesFor(body)).toBe(failingIds.length);
        for (const id of failingIds) {
          expect(body).toContain("- run " + id + " ");
        }
        expect(creates.some((row) => (row.body ?? "").includes("Opened by the offline improver."))).toBe(false);
        if (runNonce === "") {
          expect(pulls[0]?.reviewers ?? []).toContain("m6-reviewer");
        } else {
          const mine = pulls.filter((pull) => pull.head === "improve/" + batchId.trim());
          expect(mine[0]?.reviewers ?? []).toContain("m6-reviewer");
        }
        expect(guard).toBe(true);
      }
      trace.push("guard: factory/config passed");
    } else {
      const remote = JSON.parse(await fs.readFile(path.join(evidence, "remote-pr.json"), "utf8")) as {
        html_url?: string;
        head?: string;
        base?: string;
        state?: string;
      };
      expect(remote.state).toBe("open");
      expect(remote.head).toBe("improve/" + batchId.trim());
      const runBase = process.env.M6_RUN_BASE ?? "";
      expect(runBase.startsWith("m6-run/")).toBe(true);
      expect(remote.base).toBe(runBase);
      expect(remote.html_url ?? "").toMatch(/^https:/);
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
    await reloadUntil(page, async () => conventionReady(page), 15 * 60 * 1000);
    const candidate = matrixRow(page, "Candidate");
    const baseline = matrixRow(page, "Baseline");
    await expect(candidate.getByRole("rowheader")).toContainText("Winner");
    await expect(baseline.getByRole("rowheader")).not.toContainText("Winner");
    await expect.poll(async () => metricText(candidate, "n")).toBe("5");
    await expect.poll(async () => metricText(baseline, "n")).toBe("5");
    await expect.poll(async () => metricText(candidate, "Errored")).toBe("0");
    await expect.poll(async () => metricText(baseline, "Errored")).toBe("0");
    const candidateRate = await metricText(candidate, "Pass rate");
    const baselineRate = await metricText(baseline, "Pass rate");
    if (offline) {
      expect(candidateRate).toBe("100%");
      expect(baselineRate).toBe("0%");
      await expect(page.locator(".callout")).toHaveCount(0);
    } else {
      expect(candidateRate).toMatch(/^\d+(?:\.\d+)?%$/);
      expect(baselineRate).toMatch(/^\d+(?:\.\d+)?%$/);
    }
    trace.push(
      "matrix: candidate " +
        candidateRate +
        " n 5 errored 0 baseline " +
        baselineRate +
        " n 5 errored 0",
    );
    trace.push("d5: Candidate");
    await axe(page, "benchmarks");
    await saveDom(page, "benchmarks");
    await saveShot(page, "benchmarks");

    if (offline) {
      trialsLive = false;
      const factoryId = new URL(page.url()).pathname.split("/")[2] ?? "";
      await page.goto(`/f/${factoryId}/benchmarks`);
      await expect(page.getByRole("link", { name: "fault", exact: true })).toBeVisible();
      await axe(page, "benchmark-list");
      await page.getByRole("link", { name: "fault", exact: true }).click();
      await page.locator("#benchmark-repetitions").fill("1");
      await page.getByRole("button", { name: "Run benchmark" }).click();
      const accepted = page.waitForResponse(
        (response) => response.request().method() === "POST" && response.url().includes("/benchmarks/fault:run"),
      );
      await page.getByRole("dialog", { name: "Run benchmark?" }).getByRole("button", { name: "Run benchmark" }).click();
      expect((await accepted).ok()).toBe(true);
      trialsLive = true;
      await reloadUntil(
        page,
        async () => (await page.getByText("Infrastructure").count()) >= 1,
        8 * 60 * 1000,
      );
      trace.push("fault errored: infrastructure");
      await axe(page, "fault");
    }

    await openSection(page, "Scorers");
    await page.getByRole("link", { name: "Results for cite-sources" }).click();
    let runLink = page.getByRole("row").filter({ hasText: "missed" }).getByRole("link").first();
    if (runNonce !== "") {
      const allow = await runsForNonce(page);
      const rows = page.getByRole("row").filter({ hasText: "missed" });
      const count = await rows.count();
      for (let i = 0; i < count; i += 1) {
        const link = rows.nth(i).getByRole("link").first();
        const id = ((await link.innerText()) ?? "").trim();
        if (allow.has(id)) {
          runLink = link;
          break;
        }
      }
    }
    await runLink.click();
    await expect(page.getByRole("heading", { name: "Scores" })).toBeVisible();
    const scoreRow = page.getByRole("row").filter({ has: page.getByRole("rowheader", { name: "cite-sources" }) });
    const before = ((await scoreRow.locator("td").last().textContent()) ?? "").trim();
    await page.getByLabel("Scorer").selectOption("cite-sources");
    await page.getByRole("button", { name: "Score now" }).click();
    const scoreDialog = page.getByRole("dialog", { name: "Score this run?" });
    const scored = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes("/score"),
    );
    await scoreDialog.getByRole("button", { name: "Score now" }).click();
    expect((await scored).ok()).toBe(true);
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
    await axe(page, "run-detail");
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

// Empty on a default run, so titles stay the ones the offline router and
// the evidence checks already use. specs sets a hex nonce so a second
// run does not match the previous run's rows. These sit below the test
// so the reporter line stays where the default run prints it.
const runNonce = process.env.M6_RUN_NONCE ?? "";
if (runNonce !== "" && !/^[0-9a-f]+$/.test(runNonce)) {
  throw new Error("M6_RUN_NONCE must be hex");
}

function scoped(title: string): string {
  if (runNonce === "") return title;
  const space = title.indexOf(" ");
  if (space === -1) return `${title} ${runNonce}`;
  return `${title.slice(0, space)} ${runNonce}${title.slice(space)}`;
}

async function runsForNonce(page: Page): Promise<Set<string>> {
  const response = await page.request.get("/api/v1/agent/runs", {
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  const rows = (await response.json()) as { id?: string; title?: string }[];
  const ids = new Set<string>();
  for (const row of rows) {
    const title = row.title ?? "";
    const id = row.id ?? "";
    if (title.includes(runNonce) && id.startsWith("run_")) ids.add(id);
  }
  return ids;
}

async function missedIds(page: Page): Promise<string[]> {
  const missed = page.getByRole("row").filter({ hasText: "missed" });
  const allow = runNonce === "" ? null : await runsForNonce(page);
  const ids: string[] = [];
  const count = await missed.count();
  for (let i = 0; i < count; i += 1) {
    const id = ((await missed.nth(i).getByRole("link").first().innerText()) ?? "").trim();
    if (allow && !allow.has(id)) continue;
    expect(id.startsWith("run_")).toBe(true);
    ids.push(id);
  }
  return ids;
}
