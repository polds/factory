import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { setAPIKey } from "../../api/session";
import { demoAPIKey } from "../../mocks/demo-key";
import { server } from "../../mocks/server";
import { expectNoSeriousViolations } from "../../test/axe";
import { renderApp } from "../../test/render";
import identities from "./__fixtures__/identities.json";
import secrets from "./__fixtures__/secrets.json";

async function renderSecrets() {
  setAPIKey(demoAPIKey);
  return await renderApp("/f/fac_1/secrets");
}

describe("secrets", () => {
  it("lists names and identities and drops planted secret fields", async () => {
    const urls: string[] = [];
    server.events.on("request:start", ({ request }) => urls.push(request.url));
    server.use(
      http.get("/api/v1/factory/:uid/secrets", () =>
        HttpResponse.json([
          {
            ...secrets[0],
            value: "sk-PLANTED-123",
            token: "sk-PLANTED-123",
            secret_value: "sk-PLANTED-123",
          },
        ]),
      ),
      http.get("/api/v1/agent/identities", () =>
        HttpResponse.json([{ ...identities[0], token: "ghp_PLANTED" }]),
      ),
    );
    await renderSecrets();
    expect(await screen.findByRole("heading", { name: "Secrets" })).toBeInTheDocument();
    expect(
      screen.getByText(
        "Values are never shown. This page lists names, providers and presence only.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("OPENAI_API_KEY")).toBeInTheDocument();
    expect(screen.getByText("Present")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "run_1" })).toHaveAttribute("href", "/runs/run_1");
    expect(screen.getByText("factory-bot")).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Value" })).toBeNull();
    expect(document.body.innerHTML).not.toContain("sk-PLANTED-123");
    expect(document.body.innerHTML).not.toContain("ghp_PLANTED");
    expect(
      urls.map((url) => new URL(url).pathname).every((path) => path.startsWith("/api/v1")),
    ).toBe(true);
    await expectNoSeriousViolations();
  });
});
