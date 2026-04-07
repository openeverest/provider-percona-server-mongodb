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

import type { Page } from "@playwright/test";

/**
 * Page Object for the Everest login page.
 *
 * Encapsulates the login flow so that tests never deal with raw selectors for
 * the authentication form.
 */
export class LoginPage {
  constructor(private readonly page: Page) {}

  /** Navigate to the login page. */
  async goto(): Promise<void> {
    await this.page.goto("/login");
    await this.page.waitForLoadState("networkidle");
  }

  /** Fill in the username field. */
  async fillUsername(username: string): Promise<void> {
    await this.page.getByLabel("Username").fill(username);
  }

  /** Fill in the password field. */
  async fillPassword(password: string): Promise<void> {
    await this.page.getByLabel("Password").fill(password);
  }

  /** Click the submit / login button. */
  async submit(): Promise<void> {
    await this.page.getByRole("button", { name: /log in|sign in/i }).click();
  }

  /**
   * Perform a full login and wait for navigation away from the login page.
   * This is the primary helper most tests should call.
   */
  async login(username: string, password: string): Promise<void> {
    await this.goto();
    await this.fillUsername(username);
    await this.fillPassword(password);
    await this.submit();

    // Wait until we've navigated away from the login page.
    await this.page.waitForURL((url) => !url.pathname.includes("/login"), {
      timeout: 30_000,
    });
  }
}
