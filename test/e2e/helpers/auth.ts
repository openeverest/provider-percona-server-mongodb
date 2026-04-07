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

import { type Page, type BrowserContext } from "@playwright/test";
import { LoginPage } from "../pages/login.page";
import path from "node:path";
import fs from "node:fs";

/** Directory where authentication state is persisted between runs. */
const AUTH_DIR = path.join(__dirname, "..", ".auth");

/** Path to the persisted storage-state file. */
export const STORAGE_STATE_PATH = path.join(AUTH_DIR, "user.json");

/**
 * Return the credentials used for E2E tests.
 *
 * Values come from environment variables, falling back to reasonable defaults
 * for a fresh Everest installation (admin / admin).
 */
export function getCredentials(): { username: string; password: string } {
  return {
    username: process.env.EVEREST_USER ?? "admin",
    password: process.env.EVEREST_PASSWORD ?? "admin",
  };
}

/**
 * Log in through the UI and persist the resulting session to disk so that
 * subsequent tests can reuse it without logging in again.
 */
export async function authenticateAndSave(page: Page): Promise<void> {
  const { username, password } = getCredentials();
  const loginPage = new LoginPage(page);
  await loginPage.login(username, password);

  // Ensure the auth directory exists.
  fs.mkdirSync(AUTH_DIR, { recursive: true });

  // Save the full browser storage state (cookies + localStorage).
  await page.context().storageState({ path: STORAGE_STATE_PATH });
}

/**
 * Check whether a valid storage-state file already exists, allowing us to
 * skip re-authentication when running tests locally in rapid succession.
 */
export function hasStorageState(): boolean {
  return fs.existsSync(STORAGE_STATE_PATH);
}
