// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { defineConfig, devices } from "@playwright/test";

const isCI = !!process.env.CI;

/**
 * E2E test configuration for provider-percona-server-mongodb.
 *
 * Tests exercise the provider through the OpenEverest UI running on a k3d
 * cluster. The UI is expected to be available at EVEREST_URL (default
 * http://localhost:8080) via kubectl port-forward.
 */
export default defineConfig({
  testDir: "./tests",
  testMatch: "**/*.e2e.ts",
  outputDir: "./test-results",

  /* Run tests sequentially — DB provisioning is slow and stateful. */
  fullyParallel: false,
  workers: 1,

  /* Fail the build on CI if you accidentally left test.only in the source code. */
  forbidOnly: isCI,

  /* Retry on CI to absorb transient cluster/UI flakiness. */
  retries: isCI ? 2 : 0,

  /* Per-test timeout: 5 minutes (DB operations through the UI are slow). */
  timeout: 5 * 60 * 1_000,

  /* Global timeout: 30 minutes for the full suite. */
  globalTimeout: 30 * 60 * 1_000,

  reporter: [
    ["list"],
    ["html", { open: "never", outputFolder: "./playwright-report" }],
    ...(isCI ? [["github"] as const] : []),
  ],

  /* Global setup: authenticate once and persist the session. */
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",

  use: {
    baseURL: process.env.EVEREST_URL || "http://localhost:8080",
    storageState: ".auth/user.json",
    headless: true,

    /* Collect trace on first retry to help debug CI failures. */
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
