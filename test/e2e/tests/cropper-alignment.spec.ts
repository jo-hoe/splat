import { test, expect, Page } from '@playwright/test';

// Regression test for the Cropper crop-box misalignment bug.
//
// The editor builds Cropper.js on htmx:afterSwap. Because #editor is a CSS grid
// row (1fr) containing a flex column, the .cropper-wrap box is not always laid
// out when Cropper measures it, so the absolutely-positioned .cropper-container
// could land offset from the image — commonly ~one editor-row-height too low.
// Visually the photo sat at the top-left of the wrap while the crop box floated
// in empty space: "the preselection was not even on the picture." The bug
// surfaced when clicking several thumbnails in sequence, not on a single click.
//
// This test clicks the first N thumbnails one after another and asserts that,
// for each, the Cropper container is aligned with its wrap (top/left within a
// 1px tolerance) and the crop box sits inside the wrap (over the image).

const TOLERANCE = 2; // px

type Geometry = {
  key: string | null;
  // container position relative to the image wrap
  offsetTop: number | null;
  offsetLeft: number | null;
  // whether the crop box is fully within the wrap (i.e. over the image area)
  cropBoxInsideWrap: boolean | null;
};

async function cropGeometry(page: Page): Promise<Geometry> {
  return page.evaluate((tol) => {
    const r = (sel: string) => {
      const el = document.querySelector(sel);
      return el ? el.getBoundingClientRect() : null;
    };
    const wrap = r('.cropper-wrap');
    const container = r('.cropper-container');
    const box = r('.cropper-crop-box');
    const key = document.getElementById('editor-pane')?.dataset.key ?? null;
    if (!wrap || !container) {
      return { key, offsetTop: null, offsetLeft: null, cropBoxInsideWrap: null };
    }
    const cropBoxInsideWrap = box
      ? box.left >= wrap.left - tol &&
        box.top >= wrap.top - tol &&
        box.right <= wrap.right + tol &&
        box.bottom <= wrap.bottom + tol
      : null;
    return {
      key,
      offsetTop: Math.round(container.top - wrap.top),
      offsetLeft: Math.round(container.left - wrap.left),
      cropBoxInsideWrap,
    };
  }, TOLERANCE);
}

test.beforeEach(async ({ page }) => {
  await page.goto('/index.html');
  await page.waitForSelector('#strip .thumb');
});

test('crop box stays aligned with the image across sequential thumb clicks', async ({ page }) => {
  const thumbs = page.locator('#strip .thumb');
  const count = Math.min(5, await thumbs.count());
  expect(count).toBeGreaterThanOrEqual(3);

  for (let i = 0; i < count; i++) {
    const key = await thumbs.nth(i).getAttribute('data-key');
    await thumbs.nth(i).click();

    // Wait until the editor has swapped to THIS key and its cropper overlay is
    // fully built (container + crop box present). This avoids sampling during
    // the htmx teardown/rebuild window between thumbs, where the container is
    // momentarily absent.
    await page.waitForFunction(
      (expectedKey) => {
        const pane = document.getElementById('editor-pane');
        if (!pane || pane.dataset.key !== expectedKey) return false;
        return (
          !!document.querySelector('.cropper-container') &&
          !!document.querySelector('.cropper-crop-box')
        );
      },
      key,
      { timeout: 10000 }
    );

    // Assert alignment settles and holds. Poll so a late reflow that pushes the
    // container off (the original bug) is still caught — the value must settle
    // to aligned, not merely pass transiently.
    await expect
      .poll(async () => (await cropGeometry(page)).offsetTop, { timeout: 5000 })
      .toBe(0);

    const g = await cropGeometry(page);
    expect(
      Math.abs(g.offsetTop ?? 999),
      `thumb #${i} (${g.key}) container top offset from wrap`
    ).toBeLessThanOrEqual(TOLERANCE);
    expect(
      Math.abs(g.offsetLeft ?? 999),
      `thumb #${i} (${g.key}) container left offset from wrap`
    ).toBeLessThanOrEqual(TOLERANCE);
    expect(
      g.cropBoxInsideWrap,
      `thumb #${i} (${g.key}) crop box sits over the image`
    ).toBe(true);
  }
});
