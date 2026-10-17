import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import gitDefinition from "./__fixtures__/definition.json";
import localDefinition from "./__fixtures__/definition-local.json";
import type { DefinitionFile } from "../../api/types";

const localFiles = localDefinition.files as DefinitionFile[];

async function renderDefinition(path = "/f/fac_1/definition") {
  setAPIKey(demoAPIKey);
  return await renderApp(path);
}

async function expectClean() {
  const results = await axe.run(document.body);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

function pathsOf(requests: string[]): string[] {
  return requests.map((url) => new URL(url).pathname);
}

describe("definition", () => {
  it("renders a git-backed definition as read-only", async () => {
    const urls: string[] = [];
    server.events.on("request:start", ({ request }) => urls.push(request.url));
    server.use(http.get("/api/v1/factory/:uid/definition", () => HttpResponse.json(gitDefinition)));
    await renderDefinition();
    expect(await screen.findByRole("heading", { name: "Definition" })).toBeInTheDocument();
    expect(screen.getByText("Read-only. Changes go through a pull request.")).toBeInTheDocument();
    expect(screen.getByText("main")).toBeInTheDocument();
    expect(screen.getByText("1f55a9f")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Validate" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(screen.queryByRole("link", { name: /github/i })).toBeNull();
    await screen.findByText(/acme/);
    for (const box of screen.queryAllByRole("textbox")) {
      expect(box).toHaveProperty("readOnly", true);
    }
    expect(pathsOf(urls).every((path) => path.startsWith("/api/v1"))).toBe(true);
    await expectClean();
  });

  it("validates the edited tree and keeps apply disabled while errors remain", async () => {
    const user = userEvent.setup();
    const bodies: { files: DefinitionFile[] }[] = [];
    server.use(
      http.get("/api/v1/factory/:uid/definition", () => HttpResponse.json(localDefinition)),
      http.get("/api/v1/factory-files/schemas/:version", () =>
        HttpResponse.json({ documents: ["factory.schema.json"] }),
      ),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith("/definition:validate"),
        async ({ request }) => {
          bodies.push((await request.json()) as { files: DefinitionFile[] });
          return HttpResponse.json({
            diagnostics: [
              {
                file: "agents/reviewer.yaml",
                line: 14,
                severity: "error",
                message: "model is required",
              },
            ],
          });
        },
      ),
    );
    await renderDefinition();
    const factory = await screen.findByRole("textbox", { name: "Contents of factory.yaml" });
    await user.type(factory, "!");
    await user.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByRole("button", { name: /model is required/ })).toBeInTheDocument();
    expect(bodies).toHaveLength(1);
    const sent = new Map(bodies[0]?.files.map((file) => [file.path, file.content]));
    expect(sent.size).toBe(localFiles.length);
    for (const file of localFiles) {
      const content = sent.get(file.path);
      expect(content).toBeDefined();
      if (file.path === "factory.yaml") expect(content).toBe(`${file.content}!`);
      else expect(content).toBe(file.content);
    }
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
    expect(screen.getByText("Fix 1 error to apply")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /model is required/ }));
    const reviewer = await screen.findByRole("textbox", {
      name: "Contents of agents/reviewer.yaml",
    });
    await waitFor(() => expect(reviewer).toHaveFocus());
    expect(document.querySelector('[data-diagnostic-line="14"]')).not.toBeNull();
    expect(screen.getByRole("link", { name: /Schema: v1alpha1/ })).toHaveAttribute(
      "href",
      "/api/v1/factory-files/schemas/v1alpha1",
    );
    await expectClean();
  });

  it("moves through the file tree with the arrow keys", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/factory/:uid/definition", () => HttpResponse.json(localDefinition)),
      http.get("/api/v1/factory-files/schemas/:version", () => HttpResponse.json({})),
    );
    await renderDefinition();
    const tree = await screen.findByRole("tree", { name: "Definition files" });
    const factory = within(tree).getByRole("treeitem", { name: "factory.yaml" });
    factory.focus();
    await user.keyboard("{ArrowDown}");
    await waitFor(() =>
      expect(within(tree).getByRole("treeitem", { name: "agents" })).toHaveFocus(),
    );
    await user.keyboard("{ArrowDown}");
    await waitFor(() =>
      expect(within(tree).getByRole("treeitem", { name: "agents/reviewer.yaml" })).toHaveFocus(),
    );
  });

  it("applies the file diff once and refetches the definition", async () => {
    const user = userEvent.setup();
    let stored = localFiles.map((file) => ({ ...file }));
    const applied: { files: DefinitionFile[] }[] = [];
    let gets = 0;
    server.use(
      http.get("/api/v1/factory/:uid/definition", () => {
        gets += 1;
        return HttpResponse.json({ ...localDefinition, files: stored });
      }),
      http.get("/api/v1/factory-files/schemas/:version", () => HttpResponse.json({})),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith("/definition:validate"),
        () => HttpResponse.json({ diagnostics: [] }),
      ),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith("/definition:apply"),
        async ({ request }) => {
          const body = (await request.json()) as { files: DefinitionFile[] };
          applied.push(body);
          stored = body.files.map((file) => ({ path: file.path, content: file.content }));
          return HttpResponse.json({ id: "def_1", diagnostics: [] });
        },
      ),
    );
    await renderDefinition();
    const factory = await screen.findByRole("textbox", { name: "Contents of factory.yaml" });
    await user.type(factory, "!");
    await user.type(screen.getByRole("textbox", { name: "New file path" }), "notes.yaml");
    await user.click(screen.getByRole("button", { name: "Add file" }));
    await user.click(screen.getByRole("treeitem", { name: "agents/reviewer.yaml" }));
    await user.click(screen.getByRole("button", { name: "Delete file" }));
    await user.click(screen.getByRole("button", { name: "Validate" }));
    const apply = await screen.findByRole("button", { name: "Apply" });
    await waitFor(() => expect(apply).toBeEnabled());
    await user.click(apply);
    const dialog = await screen.findByRole("dialog", { name: "Apply definition" });
    expect(within(dialog).getByRole("heading", { name: "Created (1)" })).toBeInTheDocument();
    expect(within(dialog).getByText("notes.yaml")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Updated (1)" })).toBeInTheDocument();
    expect(within(dialog).getByText("factory.yaml")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Deleted (1)" })).toBeInTheDocument();
    expect(within(dialog).getByText("agents/reviewer.yaml")).toBeInTheDocument();
    await expectClean();
    const confirm = within(dialog).getByRole("button", { name: "Confirm apply" });
    await user.dblClick(confirm);
    await waitFor(() => expect(applied).toHaveLength(1));
    expect(gets).toBeGreaterThanOrEqual(2);
    const sent = new Map(applied[0]?.files.map((file) => [file.path, file.content]));
    expect(sent.has("agents/reviewer.yaml")).toBe(false);
    expect(sent.get("factory.yaml")).toContain("!");
    expect(sent.get("notes.yaml")).toBe("");
    expect(
      screen.queryByText("The server applied a different definition than your edits"),
    ).not.toBeInTheDocument();
  });

  it("reports when the server keeps a different definition", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/factory/:uid/definition", () => HttpResponse.json(localDefinition)),
      http.get("/api/v1/factory-files/schemas/:version", () => HttpResponse.json({})),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith("/definition:validate"),
        () => HttpResponse.json({ diagnostics: [] }),
      ),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith("/definition:apply"),
        () =>
          HttpResponse.json({
            id: "def_2",
            diagnostics: [
              { file: "factory.yaml", line: 1, severity: "error", message: "name is required" },
            ],
          }),
      ),
    );
    await renderDefinition();
    const factory = await screen.findByRole("textbox", { name: "Contents of factory.yaml" });
    await user.type(factory, "!");
    await user.click(screen.getByRole("button", { name: "Validate" }));
    const apply = await screen.findByRole("button", { name: "Apply" });
    await waitFor(() => expect(apply).toBeEnabled());
    await user.click(apply);
    await user.click(await screen.findByRole("button", { name: "Confirm apply" }));
    expect(
      await screen.findByText("The server applied a different definition than your edits"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /name is required/ })).toBeInTheDocument();
  });

  it("returns focus when the apply dialog closes", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/factory/:uid/definition", () => HttpResponse.json(localDefinition)),
      http.get("/api/v1/factory-files/schemas/:version", () => HttpResponse.json({})),
      http.post(
        ({ request }) => new URL(request.url).pathname.endsWith("/definition:validate"),
        () => HttpResponse.json({ diagnostics: [] }),
      ),
    );
    await renderDefinition();
    await screen.findByRole("textbox", { name: "Contents of factory.yaml" });
    await user.click(screen.getByRole("button", { name: "Validate" }));
    const apply = await screen.findByRole("button", { name: "Apply" });
    await waitFor(() => expect(apply).toBeEnabled());
    await user.click(apply);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(apply).toHaveFocus();
  });
});
