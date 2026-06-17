/* Splat frontend.
 *
 * Responsibilities (see DESIGN.md §3):
 *   - Mode toggle persistence (localStorage).
 *   - Cropper.js wiring on editor swap.
 *   - Ratio preset buttons + custom W:H input.
 *   - Apply form submission with crop coords.
 *   - Keyboard shortcuts (←/→, Enter, Esc, Delete, 1-9).
 *   - Toast machinery driven by HX-Trigger response headers.
 *   - Toast-undo delete (DELETE fires only after timer expiry).
 *
 * Exposes a single global `splat` for inline hx-vals callbacks.
 */
(function () {
  'use strict';

  let cropper = null;
  let activeImg = null;

  const splat = {
    getMode() {
      return localStorage.getItem('splat-mode') || 'inplace';
    },
    setMode(m) {
      if (m === 'inplace' || m === 'copy') {
        localStorage.setItem('splat-mode', m);
      }
    },
    getHash() {
      const pane = document.getElementById('editor-pane');
      return pane ? pane.dataset.hash || '' : '';
    },
  };
  window.splat = splat;

  /* ---------------- mode toggle ---------------- */
  function initModeToggle() {
    const radios = document.querySelectorAll('#mode-toggle input[type=radio]');
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

  /* ---------------- cropper ---------------- */
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
    };
    if (img.complete && img.naturalWidth) {
      ready();
    } else {
      img.addEventListener('load', ready, { once: true });
    }
  }

  function applyRatio(value) {
    if (!cropper) return;
    if (value === 'free') { cropper.setAspectRatio(NaN); return; }
    if (value === 'original') {
      if (!activeImg) return;
      cropper.setAspectRatio(activeImg.naturalWidth / activeImg.naturalHeight);
      return;
    }
    const m = /^(\d{1,4}):(\d{1,4})$/.exec(value);
    if (!m) return;
    const w = parseInt(m[1], 10);
    const h = parseInt(m[2], 10);
    if (w > 0 && h > 0) cropper.setAspectRatio(w / h);
  }

  function initRatioButtons() {
    document.querySelectorAll('.ratio-btn').forEach((btn) => {
      btn.addEventListener('click', () => {
        document.querySelectorAll('.ratio-btn').forEach((b) => b.classList.remove('active'));
        btn.classList.add('active');
        applyRatio(btn.dataset.ratio);
      });
    });
    const customBtn = document.getElementById('ratio-custom-apply');
    const customIn = document.getElementById('ratio-custom');
    if (customBtn && customIn) {
      const apply = () => applyRatio(customIn.value.trim());
      customBtn.addEventListener('click', apply);
      customIn.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); apply(); }
      });
    }
  }

  /* ---------------- apply form ---------------- */
  function initApplyForm() {
    const form = document.getElementById('apply-form');
    if (!form) return;
    form.addEventListener('submit', () => {
      if (!cropper) return;
      const data = cropper.getData(true); // rounded
      document.getElementById('apply-x').value = Math.max(0, data.x);
      document.getElementById('apply-y').value = Math.max(0, data.y);
      document.getElementById('apply-w').value = Math.max(1, data.width);
      document.getElementById('apply-h').value = Math.max(1, data.height);
    });
  }

  /* ---------------- delete with toast-undo ---------------- */
  function initDelete() {
    const btn = document.getElementById('delete-btn');
    if (!btn) return;
    btn.addEventListener('click', () => scheduleDelete(btn.dataset.key, btn.dataset.keyUrl));
  }

  function scheduleDelete(key, keyURL) {
    const toast = document.createElement('div');
    toast.className = 'toast toast-undo';
    toast.innerHTML = `<span>Deleting <code></code>... <small>(5s)</small></span>`;
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
            // toast triggered by HX-Trigger header on the DELETE response
            // (when triggered via htmx) — for the fetch path we mimic it.
            spawnToast('Deleted ' + key, 'success');
            // Refresh the strip and clear the editor.
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

    undo.addEventListener('click', () => {
      clearTimeout(timer);
      toast.remove();
    });
  }

  /* ---------------- toasts ---------------- */
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

  /* ---------------- keyboard shortcuts ---------------- */
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
    if (t) {
      t.click();
      t.scrollIntoView({ inline: 'center', block: 'nearest' });
    }
  }

  function initKeyboard() {
    document.addEventListener('keydown', (e) => {
      if (isTypingTarget(e.target)) return;
      switch (e.key) {
        case 'ArrowLeft':  e.preventDefault(); moveActive(-1); break;
        case 'ArrowRight': e.preventDefault(); moveActive(1); break;
        case 'Enter': {
          const form = document.getElementById('apply-form');
          if (form) { e.preventDefault(); form.requestSubmit(); }
          break;
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

  /* ---------------- editor lifecycle ---------------- */
  function initEditorAfterSwap() {
    initCropper();
    initRatioButtons();
    initApplyForm();
    initDelete();
    syncApplyMode();
  }

  document.addEventListener('htmx:afterSwap', (e) => {
    if (e.target && e.target.id === 'editor') {
      initEditorAfterSwap();
    }
  });

  document.addEventListener('DOMContentLoaded', () => {
    initModeToggle();
    initToasts();
    initKeyboard();
  });
})();
