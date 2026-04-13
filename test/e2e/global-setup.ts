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

import { chromium, type FullConfig } from "@playwright/test";
import { authenticateAndSave } from "./helpers/auth";

/**
 * Playwright global setup.
 *
 * Launches a throwaway browser, logs in to the Everest UI, and saves the
 * resulting browser storage state to `.auth/user.json`.  All subsequent test
 * workers reuse this state so they start already-authenticated.
 */
async function globalSetup(config: FullConfig): Promise<void> {
  const baseURL =
    config.projects[0]?.use?.baseURL ?? "http://localhost:8080";

  const browser = await chromium.launch();
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();

  try {
    await authenticateAndSave(page);
  } finally {
    await browser.close();
  }
}

export default globalSetup;
