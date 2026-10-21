import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  timeout: 25 * 60 * 1000,
  expect: { timeout: 20_000 },
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: process.env.FACTORY_URL ?? "http://factoryd-m6:8080",
    actionTimeout: 20_000,
  },
});
