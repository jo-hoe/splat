import { defineConfig, devices } from '@playwright/test';

// The splat app runs as a sibling docker-compose service; baseURL points at it.
// Locally you can override with SPLAT_BASE_URL=http://localhost:8080.
const baseURL = process.env.SPLAT_BASE_URL || 'http://splat:8080';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 1,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL,
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
