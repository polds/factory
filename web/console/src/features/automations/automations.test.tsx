import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { renderApp } from "../../test/render";
import automations from "./__fixtures__/automations.json";

function renderAutomations() {
  setAPIKey(demoAPIKey);
  return renderApp("/f/fac_1/automations");
}

async function expectClean() {
  const results = await axe.run(document.body);
  const blocking = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  );
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);
}

describe("automations", () => {
  it("pauses and resumes as a runtime override", async () => {
    const user = userEvent.setup();
    const urls: string[] = [];
    let paused = false;
    let pauses = 0;
    let resumes = 0;
    server.events.on("request:start", ({ request }) => urls.push(request.url));
    server.use(
      http.get("/api/v1/factory/:uid/automations", () =>
        HttpResponse.json([{ ...automations[0], runtime_paused: paused }]),
      ),
      http.post("/api/v1/factory/:uid/automations/:name/pause", () => {
        pauses += 1;
        paused = true;
        return new HttpResponse(null, { status: 204 });
      }),
      http.post("/api/v1/factory/:uid/automations/:name/resume", () => {
        resumes += 1;
        paused = false;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderAutomations();
    expect(await screen.findByRole("heading", { name: "Automations" })).toBeInTheDocument();
    expect(screen.getByText("github.label")).toBeInTheDocument();
    const enabled = screen.getAllByText("Enabled").find((element) => element.tagName === "SPAN");
    expect(enabled?.closest("button")).toBeNull();
    expect(
      screen.getByText(
        "Runtime override: survives definition applies and doesn't edit the definition.",
      ),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Pause on-label" }));
    await user.dblClick(await screen.findByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Paused at runtime")).toBeInTheDocument();
    expect(pauses).toBe(1);
    await expectClean();
    await user.click(screen.getByRole("switch", { name: "Resume on-label" }));
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(screen.queryByText("Paused at runtime")).not.toBeInTheDocument());
    expect(resumes).toBe(1);
    expect(
      urls.map((url) => new URL(url).pathname).every((path) => path.startsWith("/api/v1")),
    ).toBe(true);
  });
});
