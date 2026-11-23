// ============================================================================
// InkFetch Client Runtime — Theme, Toasts, Chapter Multi-Select & Queue UI
// ============================================================================

(function () {
  // ---- Theme persistence ----
  const THEMES = {
    dark: { name: "Sumi Ink", accent: "#c84525" },
    light: { name: "Washi Paper", accent: "#b93b1d" },
    violet: { name: "Dark Violet", accent: "#7c3aed" },
    "neon-blue": { name: "Dark Blue Neon", accent: "#00c2ff" },
    crimson: { name: "Sakura Noir", accent: "#e11d64" },
    emerald: { name: "Cyber Emerald", accent: "#10d998" },
  };
  const THEME_ORDER = Object.keys(THEMES);

  function normalizeTheme(theme) {
    return Object.prototype.hasOwnProperty.call(THEMES, theme) ? theme : "dark";
  }

  function currentTheme() {
    return normalizeTheme(document.documentElement.getAttribute("data-theme"));
  }

  window.toggleTheme = function () {
    const idx = THEME_ORDER.indexOf(currentTheme());
    const next = THEME_ORDER[(idx + 1) % THEME_ORDER.length];
    applyTheme(next);
  };

  window.setTheme = function (theme) {
    applyTheme(normalizeTheme(theme));
    closeThemeMenu();
  };

  window.toggleThemeMenu = function (btn) {
    const menu = document.getElementById("sidebar-theme-popover");
    if (!menu) return;
    const opening = !menu.classList.contains("open");
    menu.classList.toggle("open", opening);
    if (btn) btn.setAttribute("aria-expanded", opening ? "true" : "false");
  };

  function closeThemeMenu() {
    const menu = document.getElementById("sidebar-theme-popover");
    if (menu) menu.classList.remove("open");
    const trigger = document.getElementById("sidebar-theme-trigger");
    if (trigger) trigger.setAttribute("aria-expanded", "false");
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    try {
      localStorage.setItem("inkfetch-theme", theme);
    } catch (_e) {}
    syncThemeControls(theme);
  }

  function syncThemeControls(theme) {
    const meta = THEMES[theme] || THEMES.dark;
    document.querySelectorAll("[data-theme-label]").forEach((el) => {
      el.textContent = meta.name;
    });
    document.querySelectorAll("[data-theme-choice]").forEach((btn) => {
      const active = btn.dataset.themeChoice === theme;
      btn.classList.toggle("active", active);
      btn.setAttribute("aria-pressed", active ? "true" : "false");
    });
  }

  document.addEventListener("click", (e) => {
    const switcher = document.getElementById("sidebar-theme-switcher");
    if (switcher && !switcher.contains(e.target)) {
      closeThemeMenu();
    }
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeThemeMenu();
  });

  document.addEventListener("DOMContentLoaded", () => {
    syncThemeControls(currentTheme());
    initDescToggle();
    initChapterSelection();
  });

  // ---- Global Toast System (reads X-Toast from HTMX responses) ----
  document.addEventListener("htmx:afterRequest", function (evt) {
    const xhr = evt.detail && evt.detail.xhr;
    if (!xhr) return;
    const hdr = xhr.getResponseHeader("X-Toast");
    if (!hdr) return;

    try {
      showToast(JSON.parse(hdr));
    } catch (_err) {
      showToast({ message: hdr, type: "info" });
    }
  });

  window.showToast = function ({ message, type }) {
    const container = document.getElementById("toast-container");
    if (!container) return;

    const tone = type === "success" || type === "error" ? type : "info";
    const el = document.createElement("div");
    el.className = `toast toast-${tone}`;
    el.setAttribute("role", "status");

    const dot = document.createElement("span");
    dot.className = "toast-dot";
    const text = document.createElement("span");
    text.textContent = message || "Done";

    el.appendChild(dot);
    el.appendChild(text);
    container.appendChild(el);

    setTimeout(() => {
      el.style.opacity = "0";
      el.style.transition = "opacity 160ms ease";
      setTimeout(() => el.remove(), 180);
    }, 3400);
  };

  // ---- Dialog helpers ----
  window.openDialog = function (id) {
    const dlg = document.getElementById(id);
    if (dlg && typeof dlg.showModal === "function") {
      dlg.showModal();
    }
  };

  window.closeDialog = function (id) {
    const dlg = document.getElementById(id);
    if (dlg && typeof dlg.close === "function") {
      dlg.close();
    }
  };

  document.addEventListener("click", (e) => {
    if (e.target && e.target.tagName === "DIALOG" && e.target.classList.contains("modal-dialog")) {
      const rect = e.target.getBoundingClientRect();
      const inside =
        e.clientX >= rect.left &&
        e.clientX <= rect.right &&
        e.clientY >= rect.top &&
        e.clientY <= rect.bottom;
      if (!inside) e.target.close();
    }
  });

  // ---- Synopsis expand/collapse ----
  function initDescToggle() {
    const p = document.getElementById("desc");
    const btn = document.getElementById("desc-btn");
    if (!p || !btn) return;
    if (p.scrollHeight <= p.clientHeight + 2) {
      btn.style.display = "none";
    }
  }

  window.toggleDesc = function () {
    const p = document.getElementById("desc");
    const btn = document.getElementById("desc-btn");
    if (!p || !btn) return;
    const expanded = !p.classList.contains("clamped");
    p.classList.toggle("clamped", expanded);
    btn.textContent = expanded ? "Show more" : "Show less";
  };

  // ---- Manga Chapter Filtering, Sorting & Multi-Select (with Shift-Click) ----
  let chapterFilter = "all";
  let chapterQuery = "";
  let chapterOrderDesc = true;
  let lastCheckedIdx = -1;

  function getChapterRows() {
    return Array.from(document.querySelectorAll("#chapter-list .chapter-row"));
  }

  function applyChapterView() {
    const rows = getChapterRows();
    const q = chapterQuery.trim().toLowerCase();
    let visibleCount = 0;

    rows.forEach((row) => {
      const isRead = row.dataset.read === "true";
      const isDownloaded = row.dataset.downloaded === "true";
      const num = (row.dataset.num || "").toLowerCase();
      const title = (row.dataset.title || "").toLowerCase();

      let matchFilter = true;
      if (chapterFilter === "unread") matchFilter = !isRead;
      else if (chapterFilter === "downloaded") matchFilter = isDownloaded;
      else if (chapterFilter === "missing") matchFilter = !isDownloaded;

      const matchQuery = !q || num.includes(q) || title.includes(q);
      const show = matchFilter && matchQuery;

      row.classList.toggle("is-hidden", !show);
      if (show) visibleCount++;
    });

    const empty = document.getElementById("chapter-filter-empty");
    if (empty) empty.style.display = visibleCount === 0 && rows.length > 0 ? "flex" : "none";
    updateBulkBar();
  }

  window.setChapterFilter = function (mode, btn) {
    chapterFilter = mode;
    document.querySelectorAll("#chapter-filter-tabs .tab-btn").forEach((el) => {
      el.classList.toggle("active", el === btn);
    });
    applyChapterView();
  };

  window.filterChapterQuery = function (val) {
    chapterQuery = val || "";
    applyChapterView();
  };

  window.toggleChapterSort = function (btn) {
    const list = document.getElementById("chapter-list");
    if (!list) return;
    chapterOrderDesc = !chapterOrderDesc;

    const rows = getChapterRows();
    rows.sort((a, b) => {
      const na = parseFloat(a.dataset.num || "0");
      const nb = parseFloat(b.dataset.num || "0");
      return chapterOrderDesc ? nb - na : na - nb;
    });
    rows.forEach((r) => list.appendChild(r));
    if (btn) {
      btn.querySelector("[data-sort-label]").textContent = chapterOrderDesc ? "Newest" : "Oldest";
    }
  };

  function updateBulkBar() {
    const bar = document.getElementById("bulk-bar");
    const countEl = document.getElementById("bulk-count");
    const masterCheck = document.getElementById("select-all-chapters");
    if (!bar) return;

    const visibleChecks = getChapterRows()
      .filter((r) => !r.classList.contains("is-hidden"))
      .map((r) => r.querySelector(".chapter-check"))
      .filter(Boolean);
    const checked = getChapterRows()
      .map((r) => r.querySelector(".chapter-check"))
      .filter((c) => c && c.checked);

    if (countEl) countEl.textContent = `${checked.length} selected`;
    bar.classList.toggle("visible", checked.length > 0);

    if (masterCheck) {
      const visibleChecked = visibleChecks.filter((c) => c.checked).length;
      masterCheck.checked = visibleChecks.length > 0 && visibleChecked === visibleChecks.length;
      masterCheck.indeterminate = visibleChecked > 0 && visibleChecked < visibleChecks.length;
    }
  }

  function initChapterSelection() {
    const list = document.getElementById("chapter-list");
    if (!list) return;

    list.addEventListener("click", (e) => {
      const cb = e.target.closest(".chapter-check");
      if (!cb) return;

      const visibleChecks = getChapterRows()
        .filter((r) => !r.classList.contains("is-hidden"))
        .map((r) => r.querySelector(".chapter-check"))
        .filter(Boolean);
      const idx = visibleChecks.indexOf(cb);

      if (e.shiftKey && lastCheckedIdx !== -1 && idx !== -1) {
        const start = Math.min(lastCheckedIdx, idx);
        const end = Math.max(lastCheckedIdx, idx);
        for (let i = start; i <= end; i++) {
          visibleChecks[i].checked = cb.checked;
        }
      }
      lastCheckedIdx = idx;
      updateBulkBar();
    });

    // Re-sync after HTMX replaces an individual chapter row (e.g. toggling read state)
    document.body.addEventListener("htmx:afterSwap", (e) => {
      if (e.target && e.target.closest && e.target.closest("#chapter-list")) {
        applyChapterView();
      }
    });
  }

  window.toggleSelectAllChapters = function (checked) {
    getChapterRows()
      .filter((r) => !r.classList.contains("is-hidden"))
      .forEach((r) => {
        const cb = r.querySelector(".chapter-check");
        if (cb) cb.checked = checked;
      });
    updateBulkBar();
  };

  window.clearChapterSelection = function () {
    getChapterRows().forEach((r) => {
      const cb = r.querySelector(".chapter-check");
      if (cb) cb.checked = false;
    });
    lastCheckedIdx = -1;
    updateBulkBar();
  };

  window.selectChapterRangePrompt = function () {
    const fromInput = document.getElementById("quick-range-from");
    const toInput = document.getElementById("quick-range-to");
    if (!fromInput || !toInput) return;
    const from = parseFloat(fromInput.value);
    const to = parseFloat(toInput.value);
    if (Number.isNaN(from) && Number.isNaN(to)) {
      window.showToast({ message: "Enter a start or end chapter number", type: "info" });
      return;
    }
    const lo = Number.isNaN(from) ? -Infinity : from;
    const hi = Number.isNaN(to) ? Infinity : to;

    let count = 0;
    getChapterRows().forEach((r) => {
      const n = parseFloat(r.dataset.num || "NaN");
      const cb = r.querySelector(".chapter-check");
      if (cb && !Number.isNaN(n) && n >= lo && n <= hi) {
        cb.checked = true;
        count++;
      }
    });
    updateBulkBar();
    window.showToast({ message: `Selected ${count} chapter(s)`, type: "info" });
  };

  window.runBulkChapterAction = function (action) {
    const form = document.getElementById("bulk-chapters-form");
    if (!form || !window.htmx) return;

    const checked = getChapterRows()
      .map((r) => r.querySelector(".chapter-check"))
      .filter((c) => c && c.checked)
      .map((c) => c.value);

    if (checked.length === 0) {
      window.showToast({ message: "Select at least one chapter first", type: "info" });
      return;
    }

    const params = new URLSearchParams();
    params.set("action", action);
    checked.forEach((n) => params.append("chapters", n));

    fetch(form.action, {
      method: "POST",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "HX-Request": "true",
      },
      body: params.toString(),
    })
      .then((res) => {
        const hdr = res.headers.get("X-Toast");
        if (hdr) {
          try {
            window.showToast(JSON.parse(hdr));
          } catch (_e) {
            window.showToast({ message: hdr, type: "info" });
          }
        }
        if (res.headers.get("HX-Refresh") === "true") {
          window.location.reload();
        } else if (res.ok) {
          window.clearChapterSelection();
        }
      })
      .catch(() => {
        window.showToast({ message: "Bulk action failed — check network connection", type: "error" });
      });
  };

  // ---- Downloads Queue Filter & Expand State ----
  let currentJobStatus = "all";
  let currentExpandedJob = 0;

  window.filterJobs = function (status) {
    currentJobStatus = status || "all";
    const list = document.getElementById("job-list");
    if (!list) return;

    document.querySelectorAll("#job-filters button[data-status]").forEach((btn) => {
      btn.classList.toggle("active", btn.dataset.status === currentJobStatus);
    });

    if (window.htmx) {
      htmx.ajax("GET", buildJobsURL(), { target: "#job-list", swap: "innerHTML" });
    }
  };

  window.toggleJobDetail = function (id) {
    currentExpandedJob = currentExpandedJob === id ? 0 : id;
    if (window.htmx) {
      htmx.ajax("GET", buildJobsURL(), { target: "#job-list", swap: "innerHTML" });
    }
  };

  function buildJobsURL() {
    const params = new URLSearchParams();
    if (currentJobStatus && currentJobStatus !== "all") {
      params.set("status", currentJobStatus);
    }
    if (currentExpandedJob) {
      params.set("expanded", String(currentExpandedJob));
    }
    const q = params.toString();
    return q ? `/api/jobs?${q}` : "/api/jobs";
  }

  document.addEventListener("htmx:configRequest", (e) => {
    const target = e.target;
    if (!target) return;
    if (target.id === "job-list" || target.hasAttribute("data-queue-action")) {
      if (currentJobStatus && currentJobStatus !== "all") {
        e.detail.parameters["status"] = currentJobStatus;
      }
      if (currentExpandedJob && target.id === "job-list") {
        e.detail.parameters["expanded"] = currentExpandedJob;
      }
    }
  });
})();
