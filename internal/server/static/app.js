/* Splat frontend.
 *
 * Responsibilities:
 *   - Mode toggle persistence (localStorage).
 *   - Ratio persistence across images (localStorage).
 *   - Cropper.js wiring on editor swap.
 *   - Ratio preset buttons + inline custom W:H input (apply on Enter/blur).
 *   - Save button: gather crop coords → POST → auto-advance to next image.
 *   - Rotate/flip: fire-and-reload.
 *   - Keyboard shortcuts (←/→, Enter, Esc, Delete, 1-9).
 *   - Toast machinery driven by HX-Trigger response headers.
 *   - Toast-undo delete (DELETE fires only after timer expiry).
 *   - Prefetch next N images + thumbs after strip loads.
 *
 * Exposes a single global `splat` for inline hx-vals callbacks.
 */
(function () {
  'use strict';

  let cropper = null;
  let activeImg = null;

  /* ── Config read from <meta> ──────────────────────────── */
  const prefetchCount = parseInt(
    document.querySelector('meta[name=splat-prefetch-count]')?.content || '5',
    10
  );
  const deleteUndoMs = parseInt(
    document.querySelector('meta[name=splat-delete-undo-seconds]')?.content || '5',
    10
  ) * 1000;

  /* ── splat namespace ─────────────────────────────────── */
  const splat = {
    getMode() { return localStorage.getItem('splat-mode') || 'inplace'; },
    setMode(m) {
      if (m === 'inplace' || m === 'copy') localStorage.setItem('splat-mode', m);
    },
    getHash() {
      return document.getElementById('editor-pane')?.dataset.hash || '';
    },
    getRatio() { return localStorage.getItem('splat-ratio') || 'free'; },
    setRatio(value) { localStorage.setItem('splat-ratio', value); },
  };
  window.splat = splat;

  /* ── Mode toggle ─────────────────────────────────────── */
  function initModeToggle() {
    const radios = document.querySelectorAll('input[name=mode]');
    if (!radios.length) return;
    const stored = splat.getMode();
    radios.forEach((r) => {
      r.checked = r.value === stored;
      r.addEventListener('change', (e) => {
        splat.setMode(e.target.value);
        syncApplyMode();
      });
    });
    syncApplyMode();
  }

  function syncApplyMode() {
    const hidden = document.getElementById('apply-mode');
    if (hidden) hidden.value = splat.getMode();
  }

  /* ── Cropper ─────────────────────────────────────────── */
  function destroyCropper() {
    if (cropper) {
      try { cropper.destroy(); } catch (_) { /* ignore */ }
      cropper = null;
      activeImg = null;
    }
  }

  function initCropper() {
    destroyCropper();
    const img = document.getElementById('crop-target');
    if (!img) return;
    activeImg = img;
    // token guards against a stale init: if the user clicks another thumb
    // (htmx swaps in a new #crop-target) before this image's async init runs,
    // the token no longer matches the live element and we bail.
    const token = img;

    const build = () => {
      if (activeImg !== token || !token.isConnected) return; // superseded
      img.addEventListener('ready', () => {
        if (!cropper || activeImg !== token || !token.isConnected) return;
        applyRatio(splat.getRatio());
        restoreRatioUI(splat.getRatio());
      }, { once: true });
      cropper = new Cropper(img, {
        viewMode: 1,
        autoCropArea: 1,
        responsive: true,
        background: false,
        movable: false,
        zoomable: false,
      });
    };

    // Defer one frame so a freshly htmx-swapped editor has laid out before the
    // cropper is built. NOTE: the "crop box not on the image" bug (the source
    // <img> staying in document flow and shoving the overlay ~one wrap-height
    // down) is fixed in CSS — see the `.cropper-wrap:has(.cropper-container)`
    // rule in app.css — not here, so no geometry re-check/rebuild is needed.
    const start = () => requestAnimationFrame(build);
    if (img.complete && img.naturalWidth) {
      start();
    } else {
      img.addEventListener('load', start, { once: true });
    }
  }

  /* ── Ratio ───────────────────────────────────────────── */
  function applyRatio(value) {
    if (!cropper) return;
    if (value === 'free') { cropper.setAspectRatio(NaN); return; }
    if (value === 'original') {
      if (activeImg) cropper.setAspectRatio(activeImg.naturalWidth / activeImg.naturalHeight);
      return;
    }
    const m = /^(\d{1,4}):(\d{1,4})$/.exec(value);
    if (m) {
      const w = parseInt(m[1], 10), h = parseInt(m[2], 10);
      if (w > 0 && h > 0) cropper.setAspectRatio(w / h);
    }
  }

  function restoreRatioUI(value) {
    let matched = false;
    document.querySelectorAll('.ratio-btn').forEach((btn) => {
      const active = btn.dataset.ratio === value;
      btn.classList.toggle('active', active);
      if (active) matched = true;
    });
    const customIn = document.getElementById('ratio-custom');
    if (customIn) customIn.value = matched ? '' : value;
  }

  function initRatioButtons() {
    document.querySelectorAll('.ratio-btn').forEach((btn) => {
      btn.addEventListener('click', () => {
        document.querySelectorAll('.ratio-btn').forEach((b) => b.classList.remove('active'));
        btn.classList.add('active');
        const customIn = document.getElementById('ratio-custom');
        if (customIn) customIn.value = '';
        splat.setRatio(btn.dataset.ratio);
        applyRatio(btn.dataset.ratio);
      });
    });

    const customIn = document.getElementById('ratio-custom');
    if (!customIn) return;

    const applyCustom = () => {
      const value = customIn.value.trim();
      if (/^\d{1,4}:\d{1,4}$/.test(value)) {
        document.querySelectorAll('.ratio-btn').forEach((b) => b.classList.remove('active'));
        splat.setRatio(value);
        applyRatio(value);
      }
    };
    customIn.addEventListener('change', applyCustom);
    customIn.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); applyCustom(); }
    });
  }

  /* ── Save (crop) ─────────────────────────────────────── */
  // savePending is set before the Save POST and cleared once we've advanced.
  // Rotate/flip use their own htmx posts and never set this flag, so they
  // do not trigger auto-advance.
  let savePending = false;

  function initSaveButton() {
    const btn = document.getElementById('save-btn');
    if (!btn) return;
    btn.addEventListener('click', triggerSave);
  }

  function triggerSave() {
    if (!cropper) return;
    const form = document.getElementById('apply-form');
    if (!form) return;
    const data = cropper.getData(true);
    document.getElementById('apply-x').value = Math.max(0, data.x);
    document.getElementById('apply-y').value = Math.max(0, data.y);
    document.getElementById('apply-w').value = Math.max(1, data.width);
    document.getElementById('apply-h').value = Math.max(1, data.height);
    savePending = true;
    // Submit via htmx programmatically.
    window.htmx.trigger(form, 'submit');
  }

  /* ── After a successful apply (crop / rotate / flip) ─── */
  // Every in-place apply returns the success fragment with data-target set to
  // the edited key. The on-disk image changed, so the strip thumbnail for that
  // key is now stale and must be refreshed. Only the Save (crop) path, marked
  // by savePending, auto-advances to the next image.
  function onSaveSuccess() {
    const pane = document.querySelector('#editor .success[data-target]');
    if (!pane) return;
    const savedTarget = pane.dataset.target || '';

    // Refresh the edited thumbnail for in-place edits. For copy mode the target
    // is a brand-new key not yet in the strip, so refreshThumb is a no-op there.
    refreshThumb(savedTarget);

    if (!savePending) return;
    savePending = false;
    advanceToNextThumb();
    spawnUndoToast(
      'Saved ' + savedTarget.split('/').pop(),
      deleteUndoMs,
      () => { /* nothing to do on expire for saves */ },
      () => {
        // Undo: go back to the saved image in the editor.
        if (!savedTarget) return;
        const thumb = document.querySelector(`#strip .thumb[data-key="${CSS.escape(savedTarget)}"]`);
        if (thumb) activateThumb(thumb);
      }
    );
  }

  // refreshThumb forces the strip <img> for a key to re-request its thumbnail.
  // The thumb endpoint sends ETag + no-cache, so re-assigning src triggers a
  // revalidation that returns the freshly rendered image after an edit.
  function refreshThumb(key) {
    if (!key) return;
    const thumb = document.querySelector(`#strip .thumb[data-key="${CSS.escape(key)}"]`);
    const img = thumb?.querySelector('img');
    if (!img) return;
    const base = img.getAttribute('src').split('?')[0];
    img.setAttribute('src', base + '?t=' + Date.now());
  }

  function advanceToNextThumb() {
    const thumbs = Array.from(document.querySelectorAll('#strip .thumb'));
    if (!thumbs.length) return;
    const current = thumbs.findIndex((t) => t.classList.contains('active'));
    const next = current >= 0 && current + 1 < thumbs.length ? thumbs[current + 1] : null;
    if (next) activateThumb(next);
  }

  /* ── Prefetch ─────────────────────────────────────────── */
  const prefetched = new Set();

  function prefetchAfterThumb(activatedThumb) {
    if (!prefetchCount) return;
    const thumbs = Array.from(document.querySelectorAll('#strip .thumb'));
    const idx = thumbs.indexOf(activatedThumb);
    if (idx < 0) return;
    const end = Math.min(thumbs.length, idx + 1 + prefetchCount);
    for (let i = idx + 1; i < end; i++) {
      const key = thumbs[i].dataset.key;
      if (!key) continue;
      prefetchURL('/image/' + encodeKeyPath(key));
      prefetchURL('/thumb/' + encodeKeyPath(key));
    }
  }

  function prefetchURL(url) {
    if (prefetched.has(url)) return;
    prefetched.add(url);
    const link = document.createElement('link');
    link.rel = 'prefetch';
    link.href = url;
    link.as = 'image';
    document.head.appendChild(link);
  }

  function encodeKeyPath(key) {
    return key.split('/').map(encodeURIComponent).join('/');
  }

  /* ── Delete with toast-undo ──────────────────────────── */
  function initDelete() {
    const btn = document.getElementById('delete-btn');
    if (!btn) return;
    btn.addEventListener('click', () => scheduleDelete(btn.dataset.key, btn.dataset.keyUrl));
  }

  function scheduleDelete(key, keyURL) {
    // Immediately advance to next image and remove the thumb from the strip
    // so the user can keep working. The actual DELETE fires after the undo
    // window expires. Closing the tab cancels the delete.
    const thumb = document.querySelector(`#strip .thumb[data-key="${CSS.escape(key)}"]`);
    advanceToNextThumb();
    if (thumb) thumb.remove();

    // Clear the editor pane so the deleted image is no longer shown.
    const editor = document.getElementById('editor');
    if (editor && !document.getElementById('editor-pane')) {
      editor.innerHTML = '<p class="placeholder">Click an image below to edit.</p>';
    }

    spawnUndoToast(
      'Deleted ' + key.split('/').pop(),
      deleteUndoMs,
      () => {
        // Timer expired — execute the delete.
        fetch('/image/' + keyURL, { method: 'DELETE' })
          .then((res) => {
            if (!res.ok) spawnToast('Delete failed: ' + res.status, 'error');
          })
          .catch((err) => spawnToast('Delete failed: ' + err.message, 'error'));
      },
      () => {
        // Undo clicked — put the thumb back by reloading the strip.
        const strip = document.getElementById('strip');
        if (strip && window.htmx) window.htmx.trigger(strip, 'load');
      }
    );
  }

  /* ── Toasts ──────────────────────────────────────────── */
  // spawnToast shows a plain auto-dismissing notification.
  function spawnToast(message, kind) {
    const el = document.createElement('div');
    el.className = 'toast toast-' + (kind || 'success');
    const msg = document.createElement('span');
    msg.textContent = message;
    el.appendChild(msg);
    const close = document.createElement('button');
    close.type = 'button';
    close.className = 'toast-close';
    close.textContent = '×';
    close.addEventListener('click', () => el.remove());
    el.appendChild(close);
    document.getElementById('toasts').appendChild(el);
    setTimeout(() => el.remove(), 5000);
  }

  // spawnUndoToast shows a countdown notification with an Undo button
  // embedded inside the toast. onExpire fires when the timer runs out;
  // onUndo fires if the user clicks Undo (timer is cancelled).
  function spawnUndoToast(message, durationMs, onExpire, onUndo) {
    const el = document.createElement('div');
    el.className = 'toast toast-undo';

    const msg = document.createElement('span');
    msg.className = 'toast-msg';
    msg.textContent = message;
    el.appendChild(msg);

    const undo = document.createElement('button');
    undo.type = 'button';
    undo.className = 'toast-undo-btn';
    undo.textContent = 'Undo';
    el.appendChild(undo);

    const close = document.createElement('button');
    close.type = 'button';
    close.className = 'toast-close';
    close.textContent = '×';
    el.appendChild(close);

    document.getElementById('toasts').appendChild(el);

    const timer = setTimeout(() => {
      el.remove();
      onExpire();
    }, durationMs);

    undo.addEventListener('click', () => {
      clearTimeout(timer);
      el.remove();
      if (onUndo) onUndo();
    });
    close.addEventListener('click', () => {
      clearTimeout(timer);
      el.remove();
      if (onUndo) onUndo();
    });
  }

  function initToasts() {
    document.body.addEventListener('showToast', (e) => {
      const { message, kind } = e.detail || {};
      if (message) spawnToast(message, kind);
    });
  }

  /* ── Keyboard shortcuts ──────────────────────────────── */
  function isTypingTarget(el) {
    if (!el) return false;
    const tag = el.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || el.isContentEditable;
  }

  function activeThumbIndex(thumbs) {
    const pane = document.getElementById('editor-pane');
    if (!pane) return -1;
    const key = pane.dataset.key;
    return Array.from(thumbs).findIndex((t) => t.dataset.key === key);
  }

  function moveActive(delta) {
    const thumbs = document.querySelectorAll('#strip .thumb');
    if (!thumbs.length) return;
    const cur = activeThumbIndex(thumbs);
    const next = Math.max(0, Math.min(thumbs.length - 1, (cur < 0 ? 0 : cur + delta)));
    const t = thumbs[next];
    if (t) activateThumb(t);
  }

  function initKeyboard() {
    document.addEventListener('keydown', (e) => {
      if (isTypingTarget(e.target)) return;
      switch (e.key) {
        case 'ArrowLeft':  e.preventDefault(); moveActive(-1); break;
        case 'ArrowRight': e.preventDefault(); moveActive(1); break;
        case 'Enter': {
          e.preventDefault(); triggerSave(); break;
        }
        case 'Escape':
          if (cropper) { e.preventDefault(); cropper.clear(); }
          break;
        case 'Delete':
        case 'Backspace': {
          const btn = document.getElementById('delete-btn');
          if (btn) { e.preventDefault(); btn.click(); }
          break;
        }
        default: {
          if (e.key >= '1' && e.key <= '9') {
            const idx = parseInt(e.key, 10) - 1;
            const buttons = document.querySelectorAll('.ratio-btn');
            if (buttons[idx]) { e.preventDefault(); buttons[idx].click(); }
          }
        }
      }
    });
  }

  /* ── Thumb click wiring ───────────────────────────────── */
  // Mark the given thumb active, kick off prefetch, and move DOM focus to it.
  // The .active class (visual), DOM focus (keyboard/scroll anchor), and the
  // editor pane's data-key must all point at the same thumb; this is the one
  // place that keeps them in sync.
  function setActiveClass(thumb) {
    document.querySelectorAll('#strip .thumb.active').forEach((t) => t.classList.remove('active'));
    thumb.classList.add('active');
  }

  // activateThumb drives selection programmatically (keyboard nav, auto-advance,
  // undo). A programmatic element.click() does NOT move DOM focus, so we focus
  // explicitly — otherwise focus stays orphaned on the last mouse-clicked thumb.
  // The click() fires onThumbClick (which sets .active + prefetches) and triggers
  // the htmx hx-get that swaps the editor.
  function activateThumb(thumb) {
    if (!thumb) return;
    thumb.focus();
    thumb.scrollIntoView({ inline: 'center', block: 'nearest' });
    thumb.click();
  }

  // onThumbClick handles real user clicks. The browser already moved focus to
  // the thumb, so we only sync the .active class and prefetch.
  function onThumbClick(e) {
    const thumb = e.currentTarget;
    setActiveClass(thumb);
    prefetchAfterThumb(thumb);
  }

  // After a strip reload (undo, lazy "revealed" load) the server re-renders the
  // thumbs with no .active class and no focus, so the previously selected thumb
  // is destroyed and focus falls back to <body>. Re-apply both from the editor
  // pane's data-key, which survives the reload.
  function syncActiveThumb() {
    const key = document.getElementById('editor-pane')?.dataset.key;
    if (!key) return;
    const thumb = document.querySelector(`#strip .thumb[data-key="${CSS.escape(key)}"]`);
    if (!thumb) return;
    setActiveClass(thumb);
    // Only restore focus if it was lost to <body> during the swap, so we don't
    // steal focus from an input the user may be typing in.
    if (document.activeElement === document.body || document.activeElement === null) {
      thumb.focus();
    }
  }

  function attachThumbListeners() {
    document.querySelectorAll('#strip .thumb').forEach((t) => {
      // Avoid double-binding on re-renders.
      if (t.dataset.splatBound) return;
      t.dataset.splatBound = '1';
      t.addEventListener('click', onThumbClick);
    });
    syncActiveThumb();
  }

  /* ── Editor lifecycle ─────────────────────────────────── */
  function initEditorAfterSwap() {
    initCropper();
    initRatioButtons();
    initSaveButton();
    initDelete();
    syncApplyMode();
    onSaveSuccess();
  }

  document.addEventListener('htmx:afterSwap', (e) => {
    if (!e.target) return;
    if (e.target.id === 'editor') {
      initEditorAfterSwap();
    }
    if (e.target.id === 'strip') {
      attachThumbListeners();
    }
  });

  document.addEventListener('DOMContentLoaded', () => {
    initModeToggle();
    initToasts();
    initKeyboard();
  });
})();
