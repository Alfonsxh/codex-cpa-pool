import { expect } from "@playwright/test";

// Captures can queue under concurrent rendering; keep ordinary UI waits and
// all screenshot comparison thresholds unchanged.
export const screenshotExpect = expect.configure({ timeout: 30_000 });
