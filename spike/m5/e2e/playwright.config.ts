import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  timeout: 20 * 60 * 1000,
  expect: { timeout: 20_000 },
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: process.env.FACTORY_URL ?? "http://factoryd:8080",
  },
});
