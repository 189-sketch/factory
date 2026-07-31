import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    include: ["test/**/*.test.ts"],
    // better-sqlite3 is synchronous and the registry uses a real temp-file DB;
    // run serially to avoid cross-test file contention.
    pool: "forks",
    testTimeout: 20000,
    hookTimeout: 20000,
  },
});
