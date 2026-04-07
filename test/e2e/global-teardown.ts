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

import fs from "node:fs";
import path from "node:path";

/**
 * Playwright global teardown.
 *
 * Clean up any persistent artifacts created during the test run (e.g. the
 * saved auth state).  Extend this function when you add test resources that
 * need cluster-level cleanup.
 */
async function globalTeardown(): Promise<void> {
  const authDir = path.join(__dirname, ".auth");
  if (fs.existsSync(authDir)) {
    fs.rmSync(authDir, { recursive: true, force: true });
  }
}

export default globalTeardown;
