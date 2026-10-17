import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { SESSION_KEY } from "../api/session";
import { demoAPIKey } from "../mocks/demo-key";
import { server } from "../mocks/server";
import { renderApp } from "./render";

describe("login", () => {
  it("stores the API key and returns to login after a 401", async () => {
    const user = userEvent.setup();
    const { queryClient } = renderApp("/");
    const field = await screen.findByLabelText("API key");

    await user.type(field, "wrong-key");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("That API key was rejected.");
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
    expect(document.body.textContent).not.toContain("wrong-key");

    await user.clear(screen.getByLabelText("API key"));
    await user.type(screen.getByLabelText("API key"), demoAPIKey);
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("navigation", { name: "Sections" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /payments/ })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
    expect(sessionStorage.getItem(SESSION_KEY)).toBe(demoAPIKey);
    expect(document.body.textContent).not.toContain(demoAPIKey);

    server.use(
      http.get("/api/v1/factory", () =>
        HttpResponse.json(
          { title: "unauthorized", status: 401, detail: "unauthorized" },
          { status: 401 },
        ),
      ),
    );
    await queryClient.invalidateQueries({ queryKey: ["factories"] });
    expect(await screen.findByLabelText("API key")).toBeInTheDocument();
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
  });
});
