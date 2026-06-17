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
    const ready = () => {
      cropper = new Cropper(img, {
        viewMode: 1,
        autoCropArea: 1,
        responsive: true,
        background: false,
        movable: false,
        zoomable: false,
      });
      img.addEventListener('ready', () => {
        applyRatio(splat.getRatio());
        restoreRatioUI(splat.getRatio());
      }, { once: true });
    };
    if (img.complete && img.naturalWidth) {
      ready();
    } else {
      img.addEventListener('load', ready, { once: true });
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
    // Submit via htmx programmatically.
    window.htmx.trigger(form, 'submit');
  }

  /* ── Auto-advance after save ─────────────────────────── */
  // Called when htmx swaps in a success fragment for #editor.
  // The success fragment has data-target; we advance to the next thumb.
  function onSaveSuccess() {
    const pane = document.querySelector('#editor .success[data-target]');
    if (!pane) return;
    advanceToNextThumb();
  }

  function advanceToNextThumb() {
    const thumbs = Array.from(document.querySelectorAll('#strip .thumb'));
    if (!thumbs.length) return;
    const current = thumbs.findIndex((t) => t.classList.contains('active'));
    const next = current >= 0 && current + 1 < thumbs.length ? thumbs[current + 1] : null;
    if (next) {
      next.click();
      next.scrollIntoView({ inline: 'center', block: 'nearest' });
    }
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
    const toast = document.createElement('div');
    toast.className = 'toast toast-undo';
    toast.innerHTML = `<span>Deleting <code></code>…</span>`;
    toast.querySelector('code').textContent = key;
    const undo = document.createElement('button');
    undo.type = 'button';
    undo.textContent = 'Undo';
    toast.appendChild(undo);
    document.getElementById('toasts').appendChild(toast);

    const timer = setTimeout(() => {
      toast.remove();
      fetch('/image/' + keyURL, { method: 'DELETE' })
        .then((res) => {
          if (res.ok) {
            spawnToast('Deleted ' + key.split('/').pop(), 'success');
            const editor = document.getElementById('editor');
            if (editor) editor.innerHTML = '<p class="placeholder">Click an image below to edit.</p>';
            const strip = document.getElementById('strip');
            if (strip && window.htmx) window.htmx.trigger(strip, 'load');
          } else {
            spawnToast('Delete failed: ' + res.status, 'error');
          }
        })
        .catch((err) => spawnToast('Delete failed: ' + err.message, 'error'));
    }, 5000);

    undo.addEventListener('click', () => { clearTimeout(timer); toast.remove(); });
  }

  /* ── Toasts ──────────────────────────────────────────── */
  function spawnToast(message, kind) {
    const el = document.createElement('div');
    el.className = 'toast toast-' + (kind || 'success');
    el.textContent = message;
    const close = document.createElement('button');
    close.type = 'button';
    close.textContent = '×';
    close.addEventListener('click', () => el.remove());
    el.appendChild(close);
    document.getElementById('toasts').appendChild(el);
    setTimeout(() => el.remove(), 5000);
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
    if (t) { t.click(); t.scrollIntoView({ inline: 'center', block: 'nearest' }); }
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
  // Mark the clicked thumb active and kick off prefetch.
  function onThumbClick(e) {
    const thumb = e.currentTarget;
    document.querySelectorAll('#strip .thumb').forEach((t) => t.classList.remove('active'));
    thumb.classList.add('active');
    prefetchAfterThumb(thumb);
  }

  function attachThumbListeners() {
    document.querySelectorAll('#strip .thumb').forEach((t) => {
      // Avoid double-binding on re-renders.
      if (t.dataset.splatBound) return;
      t.dataset.splatBound = '1';
      t.addEventListener('click', onThumbClick);
    });
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
