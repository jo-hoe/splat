import { test, expect, Page } from '@playwright/test';

// Regression tests for two frontend bugs:
//   1. Focus desync — DOM focus, the .active class, and #editor-pane[data-key]
//      must stay pointing at the same thumb across clicks, arrow-key nav, and
//      strip reloads. Programmatic .click() does not move focus, so the app
//      focuses thumbs explicitly; a strip reload re-syncs from the editor key.
//   2. Stale preview — after an in-place edit the strip thumbnail must refresh
//      rather than showing the pre-edit cached image.

// Reads the three "selected image" signals that must agree.
async function selectionState(page: Page) {
  return page.evaluate(() => {
    const ae = document.activeElement as HTMLElement | null;
    const active = Array.from(
      document.querySelectorAll<HTMLElement>('#strip .thumb.active')
    ).map((t) => t.dataset.key);
    return {
      focusKey: ae ? ae.getAttribute('data-key') : null,
      focusTag: ae ? ae.tagName : null,
      editorKey: document.getElementById('editor-pane')?.dataset.key ?? null,
      activeKeys: active,
    };
  });
}

// Waits until the strip has rendered its thumbs.
async function waitForStrip(page: Page) {
  await page.waitForSelector('#strip .thumb');
}

test.beforeEach(async ({ page }) => {
  await page.goto('/index.html');
  await waitForStrip(page);
});

test('focus, .active, and editor key stay in sync across click + arrow keys', async ({ page }) => {
  const thumbs = page.locator('#strip .thumb');
  const firstKey = await thumbs.first().getAttribute('data-key');

  // Real click selects the first thumb and loads the editor.
  await thumbs.first().click();
  await expect(page.locator('#editor-pane')).toHaveAttribute('data-key', firstKey!);

  let s = await selectionState(page);
  expect(s.editorKey).toBe(firstKey);
  expect(s.activeKeys).toEqual([firstKey]);
  expect(s.focusKey).toBe(firstKey);

  // Arrow-right navigates. Focus must follow the newly selected thumb, not stay
  // orphaned on the originally clicked one (the original bug).
  for (let i = 0; i < 2; i++) {
    const prevEditorKey = (await selectionState(page)).editorKey;
    await page.keyboard.press('ArrowRight');
    await expect
      .poll(async () => (await selectionState(page)).editorKey)
      .not.toBe(prevEditorKey);

    s = await selectionState(page);
    expect(s.activeKeys).toHaveLength(1);
    // All three signals agree.
    expect(s.activeKeys[0]).toBe(s.editorKey);
    expect(s.focusKey).toBe(s.editorKey);
    // Focus is on a thumb, never dropped to <body>.
    expect(s.focusTag).toBe('DIV');
  }
});

test('strip reload preserves the active thumb and restores focus', async ({ page }) => {
  const thumbs = page.locator('#strip .thumb');
  // Select the third image so a reload resetting to "first" would be visible.
  await thumbs.nth(2).click();
  const selectedKey = await page.locator('#editor-pane').getAttribute('data-key');
  expect(selectedKey).toBeTruthy();

  // Trigger the same strip reload htmx performs on undo / lazy load.
  await page.evaluate(() => {
    (window as any).htmx.trigger(document.getElementById('strip'), 'load');
  });

  // After the reload the active class + focus must land back on the editor's
  // current key — not <body>, not the first thumb.
  await expect
    .poll(async () => (await selectionState(page)).activeKeys)
    .toEqual([selectedKey]);

  const s = await selectionState(page);
  expect(s.editorKey).toBe(selectedKey);
  expect(s.focusKey).toBe(selectedKey);
  expect(s.focusTag).toBe('DIV');
});

test('in-place crop refreshes the edited strip thumbnail', async ({ page }) => {
  const thumbs = page.locator('#strip .thumb');
  await thumbs.first().click();
  const editedKey = await page.locator('#editor-pane').getAttribute('data-key');
  expect(editedKey).toBeTruthy();

  // Capture the thumb's <img> src before the edit.
  const thumbImg = page.locator(
    `#strip .thumb[data-key="${editedKey}"] img`
  );
  const srcBefore = await thumbImg.getAttribute('src');

  // Expect a fresh thumbnail request for this key after the edit.
  const keyUrl = editedKey!.split('/').map(encodeURIComponent).join('/');
  const thumbResponse = page.waitForResponse(
    (r) => r.url().includes('/thumb/' + keyUrl) && r.status() === 200
  );

  // Wait for the cropper overlay to be ready, then Save (in-place crop). The
  // #crop-target <img> is display-hidden by Cropper (0x0), so wait on the crop
  // box instead — waiting for #crop-target to be "visible" would hang.
  await page.waitForSelector('.cropper-crop-box');
  await page.locator('#save-btn').click();

  await thumbResponse;

  // The <img> src must have been cache-busted so the browser re-fetches.
  await expect
    .poll(async () => thumbImg.getAttribute('src'))
    .not.toBe(srcBefore);
  expect(await thumbImg.getAttribute('src')).toContain('?t=');
});
