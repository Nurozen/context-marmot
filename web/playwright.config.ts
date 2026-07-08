import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  retries: process.env.CI ? 2 : 0,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:3299',
  },
  webServer: {
    command: 'bash e2e/serve.sh 3299',
    url: 'http://127.0.0.1:3299/api/version',
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
