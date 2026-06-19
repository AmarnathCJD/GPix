(function () {
  var tt = document.getElementById('theme-toggle');
  if (tt) {
    tt.addEventListener('click', function () {
      var cur = document.documentElement.dataset.theme;
      var matchDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
      var next;
      if (cur === 'dark') next = 'light';
      else if (cur === 'light') next = 'dark';
      else next = matchDark ? 'light' : 'dark';
      document.documentElement.dataset.theme = next;
      localStorage.setItem('theme', next);
    });
  }

  var dz = document.querySelector('.drop-zone');
  var fi = document.getElementById('upload-files');
  var summary = document.getElementById('file-summary');
  if (dz && fi) {
    function updateSummary() {
      var count = fi.files ? fi.files.length : 0;
      if (!summary) return;
      if (!count) summary.textContent = 'Multiple files supported · 5 GB max';
      else if (count === 1) summary.textContent = fi.files[0].name;
      else summary.textContent = count + ' files selected';
    }
    ['dragenter', 'dragover'].forEach(function (ev) {
      dz.addEventListener(ev, function (e) { e.preventDefault(); dz.classList.add('is-dragging'); });
    });
    ['dragleave', 'drop'].forEach(function (ev) {
      dz.addEventListener(ev, function (e) { e.preventDefault(); dz.classList.remove('is-dragging'); });
    });
    dz.addEventListener('drop', function (e) {
      if (e.dataTransfer && e.dataTransfer.files) { fi.files = e.dataTransfer.files; updateSummary(); }
    });
    fi.addEventListener('change', updateSummary);
  }

  var grid = document.querySelector('.media-grid');
  var bar = document.getElementById('action-bar');
  var toggle = document.getElementById('select-toggle');
  if (grid && bar) {
    var cards = Array.prototype.slice.call(grid.querySelectorAll('.media-card'));
    var selected = new Set();
    var selecting = false;
    var status = bar.querySelector('[data-bar-status]');
    var countEl = bar.querySelector('[data-sel-count]');
    var toggleLabel = toggle ? toggle.querySelector('[data-select-label]') : null;

    function keyOf(card) { return card.getAttribute('data-key'); }

    function syncCard(card) {
      var on = selected.has(keyOf(card));
      card.classList.toggle('is-selected', on);
      var cb = card.querySelector('.card-select');
      if (cb) cb.checked = on;
      card.setAttribute('aria-pressed', on ? 'true' : 'false');
    }

    function render() {
      document.body.classList.toggle('is-selecting', selecting);
      if (countEl) countEl.textContent = String(selected.size);
      bar.hidden = !selecting;
      if (toggle) {
        toggle.setAttribute('aria-pressed', selecting ? 'true' : 'false');
        if (toggleLabel) toggleLabel.textContent = selecting ? 'Done' : 'Select';
      }
    }

    function setStatus(text) {
      if (!status) return;
      if (text) { status.textContent = text; status.hidden = false; }
      else { status.textContent = ''; status.hidden = true; }
    }

    function enter() { if (!selecting) { selecting = true; render(); } }

    function clearSel() {
      selected.clear();
      cards.forEach(syncCard);
      render();
    }

    function exit() {
      selecting = false;
      selected.clear();
      cards.forEach(syncCard);
      setStatus('');
      render();
    }

    function toggleCard(card) {
      var k = keyOf(card);
      if (selected.has(k)) selected.delete(k);
      else selected.add(k);
      syncCard(card);
      render();
    }

    if (toggle) {
      toggle.addEventListener('click', function () {
        if (selecting) exit(); else enter();
      });
    }

    cards.forEach(function (card) {
      card.addEventListener('click', function (e) {
        if (!selecting) return;
        e.preventDefault();
        toggleCard(card);
      });

      var lpTimer = null;
      card.addEventListener('touchstart', function () {
        lpTimer = setTimeout(function () {
          enter();
          toggleCard(card);
        }, 500);
      }, { passive: true });
      ['touchend', 'touchmove', 'touchcancel'].forEach(function (ev) {
        card.addEventListener(ev, function () {
          if (lpTimer) { clearTimeout(lpTimer); lpTimer = null; }
        }, { passive: true });
      });
    });

    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && selecting) exit();
    });

    function action(name) { return bar.querySelector('[data-action="' + name + '"]'); }

    var selectAllBtn = action('select-all');
    if (selectAllBtn) selectAllBtn.addEventListener('click', function () {
      cards.forEach(function (card) { selected.add(keyOf(card)); syncCard(card); });
      render();
    });

    var clearBtn = action('clear');
    if (clearBtn) clearBtn.addEventListener('click', clearSel);

    var downloadBtn = action('download');
    if (downloadBtn) downloadBtn.addEventListener('click', function () {
      if (!selected.size) return;
      setStatus('Preparing…');
      var keys = Array.from(selected).join(',');
      window.location.href = '/api/bulk/download?keys=' + encodeURIComponent(keys);
      setTimeout(function () { setStatus(''); }, 4000);
    });

    var deleteBtn = action('delete');
    if (deleteBtn) deleteBtn.addEventListener('click', function () {
      if (!selected.size) return;
      var n = selected.size;
      if (!window.confirm('Move ' + n + ' item' + (n === 1 ? '' : 's') + ' to trash?')) return;
      setStatus('Deleting…');
      deleteBtn.disabled = true;
      fetch('/api/bulk/delete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ keys: Array.from(selected), permanent: false })
      }).then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      }).then(function (res) {
        var errs = res.errors ? Object.keys(res.errors).length : 0;
        if (errs) { setStatus(res.deleted + ' deleted, ' + errs + ' failed'); deleteBtn.disabled = false; return; }
        var cursor = bar.getAttribute('data-cursor');
        window.location.href = cursor ? '/browse?cursor=' + encodeURIComponent(cursor) : '/browse';
      }).catch(function (err) {
        setStatus('Delete failed: ' + err.message);
        deleteBtn.disabled = false;
      });
    });

    cards.forEach(syncCard);
    render();
  }

  var menuBtn = document.getElementById('menu-toggle');
  if (menuBtn) {
    menuBtn.addEventListener('click', function () {
      document.body.classList.toggle('is-nav-open');
    });
    document.addEventListener('click', function (e) {
      if (!document.body.classList.contains('is-nav-open')) return;
      var inside = e.target.closest('.sidebar') || e.target.closest('#menu-toggle');
      if (!inside) document.body.classList.remove('is-nav-open');
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') document.body.classList.remove('is-nav-open');
    });
  }

  var path = window.location.pathname;
  var sidebarLinks = document.querySelectorAll('.sidebar-link[data-page]');
  sidebarLinks.forEach(function (a) {
    var p = a.getAttribute('data-page');
    var match = (p === 'library' && (path === '/' || path === '/browse' || path.indexOf('/view/') === 0))
             || (p === 'upload' && path === '/upload');
    if (match) a.classList.add('is-active');
  });
})();
