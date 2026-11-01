import { describe, expect, it } from "vitest";

import { docs, getDoc, rewriteHref } from "./docs.ts";

describe("docs", () => {
  it("puts the README first and the numbered docs after it", () => {
    expect(docs[0]?.slug).toBe("readme");
    expect(docs[0]?.label).toBe("Overview");
    expect(docs.slice(1).map((d) => d.file)).toEqual(
      docs
        .slice(1)
        .map((d) => d.file)
        .sort(),
    );
    expect(docs.length).toBeGreaterThan(1);
  });

  it("finds a doc by slug", () => {
    expect(getDoc("02-architecture")?.label).toMatch(/architecture/i);
    expect(getDoc("nope")).toBeUndefined();
  });
});

describe("rewriteHref", () => {
  it("passes absolute URLs and anchors through", () => {
    expect(rewriteHref("https://example.com/a.md", "readme")).toBe("https://example.com/a.md");
    expect(rewriteHref("#deviation", "03-definition-schema")).toBe("#deviation");
    expect(rewriteHref(undefined, "readme")).toBeUndefined();
  });

  it("maps README links into docs/ to site routes", () => {
    expect(rewriteHref("docs/02-architecture.md", "readme")).toBe("/docs/02-architecture");
    expect(rewriteHref("docs/12-build-sequence.md#m6--measure", "readme")).toBe(
      "/docs/12-build-sequence#m6--measure",
    );
  });

  it("maps sibling links between docs to site routes", () => {
    expect(rewriteHref("04-protocols-and-apis.md", "02-architecture")).toBe(
      "/docs/04-protocols-and-apis",
    );
    expect(rewriteHref("./01-evidence-and-provenance.md#x", "02-architecture")).toBe(
      "/docs/01-evidence-and-provenance#x",
    );
    expect(rewriteHref("../README.md", "02-architecture")).toBe("/docs/readme");
  });

  it("sends other repository paths to GitHub", () => {
    expect(rewriteHref("spike/m0", "readme")).toBe(
      "https://github.com/polds/factory/tree/main/spike/m0",
    );
    expect(rewriteHref("spike/m2/README.md", "readme")).toBe(
      "https://github.com/polds/factory/blob/main/spike/m2/README.md",
    );
    expect(rewriteHref("../deploy/docker-compose.yml", "06-execution-backends")).toBe(
      "https://github.com/polds/factory/blob/main/deploy/docker-compose.yml",
    );
    expect(rewriteHref("../spike/m0", "01-evidence-and-provenance")).toBe(
      "https://github.com/polds/factory/tree/main/spike/m0",
    );
  });
});
