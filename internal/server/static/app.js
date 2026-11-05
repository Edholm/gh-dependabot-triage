// The page is rendered on the server. This script keeps it current without reloading it, so that the selection
// survives, and drives the selection and the merge queue island.
(() => {
  // Elements marked js-only do nothing without this script, so they stay hidden until it runs.
  document.documentElement.classList.add("js");

  const refreshQuickMs = 2 * 1000;
  const refreshSlowMs = 30 * 1000;
  const queueBusyMs = 2 * 1000;
  const queueIdleMs = 10 * 1000;
  const selectionKey = "selection";

  const island = document.querySelector(".island");

  // --- Selection ---------------------------------------------------------------------------------------------------

  // Storage can be unavailable (blocked cookies, private windows), in which case the selection only lasts for this
  // page. It is kept per tab, so that it survives sorting and filtering, which load a new page.
  const loadSelection = () => {
    try {
      const saved = JSON.parse(sessionStorage.getItem(selectionKey) ?? "[]");
      return Array.isArray(saved) ? saved : [];
    } catch {
      return [];
    }
  };

  const saveSelection = () => {
    try {
      sessionStorage.setItem(selectionKey, JSON.stringify([...selected]));
    } catch {
      // Ignored: see loadSelection.
    }
  };

  const selected = new Set(loadSelection());
  let lastClicked = null;

  const rows = () => [...document.querySelectorAll("tbody tr[data-key]")];

  // selectedKeys returns the selected pull requests in the order the table shows them, which is the merge order.
  const selectedKeys = () => rows().map((row) => row.dataset.key).filter((key) => selected.has(key));

  // syncSelection drops pull requests that are no longer shown from the selection and updates the checkboxes.
  const syncSelection = () => {
    const shown = new Set(rows().map((row) => row.dataset.key));
    for (const key of selected) {
      if (!shown.has(key)) {
        selected.delete(key);
      }
    }
    saveSelection();

    for (const row of rows()) {
      const isSelected = selected.has(row.dataset.key);
      row.classList.toggle("selected", isSelected);
      const checkbox = row.querySelector(".select-pr");
      if (checkbox) {
        checkbox.checked = isSelected;
      }
    }
    const all = document.querySelector(".select-all");
    if (all) {
      all.checked = shown.size > 0 && selected.size === shown.size;
      all.indeterminate = selected.size > 0 && selected.size < shown.size;
    }
    render();
  };

  const select = (keys, on) => {
    for (const key of keys) {
      if (on) {
        selected.add(key);
      } else {
        selected.delete(key);
      }
    }
    syncSelection();
  };

  document.addEventListener("click", (event) => {
    const checkbox = event.target.closest(".select-pr");
    if (checkbox) {
      const keys = rows().map((row) => row.dataset.key);
      const at = keys.indexOf(checkbox.value);
      // Shift-click selects or deselects everything between the last clicked pull request and this one.
      const from = event.shiftKey && lastClicked !== null ? keys.indexOf(lastClicked) : -1;
      if (from >= 0) {
        select(keys.slice(Math.min(from, at), Math.max(from, at) + 1), checkbox.checked);
      } else {
        select([checkbox.value], checkbox.checked);
      }
      lastClicked = checkbox.value;
      return;
    }
    if (event.target.closest(".select-all")) {
      select(rows().map((row) => row.dataset.key), event.target.checked);
      return;
    }
    const tool = event.target.closest("[data-select]");
    if (tool) {
      const all = rows();
      select(all.map((row) => row.dataset.key), false);
      if (tool.dataset.select === "all") {
        select(all.map((row) => row.dataset.key), true);
      } else if (tool.dataset.select === "passing") {
        select(all.filter((row) => row.hasAttribute("data-passing")).map((row) => row.dataset.key), true);
      }
    }
  });

  // --- Queue -------------------------------------------------------------------------------------------------------

  let queue = { items: [], busy: false };
  let sending = false;
  let error = "";
  let queueOpen = true;

  const post = async (path, body) => {
    const response = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      throw new Error((await response.text()).trim() || response.statusText);
    }
    return response.json();
  };

  // send runs a queue request and shows its outcome.
  const send = async (path, body) => {
    sending = true;
    error = "";
    render();
    try {
      setQueue(await post(path, body));
      return true;
    } catch (err) {
      error = err.message;
      return false;
    } finally {
      sending = false;
      render();
    }
  };

  const enqueue = async (action) => {
    const keys = selectedKeys();
    if (keys.length === 0) {
      return;
    }
    if (action === "merge") {
      const what = keys.length === 1 ? keys[0] : `${keys.length} pull requests`;
      if (!confirm(`Approve and merge ${what}, one at a time?`)) {
        return;
      }
    }
    if (await send("/queue", { action, pullRequests: keys })) {
      queueOpen = true;
      select(keys, false);
    }
  };

  const finishedCount = (q) => q.items.filter((item) => item.finished).length;

  const setQueue = (next) => {
    const finishedBefore = finishedCount(queue);
    queue = next;
    // Approving and merging change the pull requests, and the server is fetching them again by now. Fetching the page
    // picks up that the server is busy, which makes it refresh quickly until the new state is in.
    if (finishedCount(queue) > finishedBefore) {
      refreshPage();
    }
    render();
  };

  const pollQueue = async () => {
    try {
      const response = await fetch("/queue", { cache: "no-store" });
      if (response.ok) {
        setQueue(await response.json());
      }
    } catch {
      // The server is gone or restarting; try again later.
    }
    setTimeout(pollQueue, queue.busy ? queueBusyMs : queueIdleMs);
  };

  // --- Island ------------------------------------------------------------------------------------------------------

  // el builds an element. Children are elements or strings; strings are always inserted as text.
  const el = (tag, attrs = {}, ...children) => {
    const node = document.createElement(tag);
    for (const [name, value] of Object.entries(attrs)) {
      if (value === false || value === null || value === undefined) {
        continue;
      }
      if (name.startsWith("on")) {
        node.addEventListener(name.slice(2), value);
      } else {
        node.setAttribute(name, value === true ? "" : value);
      }
    }
    node.append(...children.filter((child) => child !== null && child !== undefined && child !== false));
    return node;
  };

  const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

  const selectionBar = () => {
    const count = selectedKeys().length;
    if (count === 0) {
      return null;
    }
    return el(
      "div",
      { class: "island-bar" },
      el("span", { class: "count" }, `${plural(count, "pull request")} selected`),
      el("span", { class: "actions" },
        el("button", { type: "button", disabled: sending, onclick: () => enqueue("approve") }, "Approve"),
        el(
          "button",
          {
            type: "button",
            class: "primary",
            disabled: sending,
            title: "Approve, then merge one at a time per repository, rebasing onto the base branch when behind",
            onclick: () => enqueue("merge"),
          },
          "Approve & merge",
        ),
        el(
          "button",
          { type: "button", class: "ghost", "aria-label": "Clear selection", onclick: () => select(selectedKeys(), false) },
          "✕",
        ),
      ),
    );
  };

  const queueItem = (item) =>
    el(
      "li",
      { class: item.finished ? "finished" : null },
      el("span", { class: `badge ${item.class}` }, item.label),
      el(
        "a",
        { href: item.url, target: "_blank", rel: "noopener noreferrer", title: item.title },
        el("span", { class: "repo-name" }, item.repository.split("/").pop()),
        el("span", { class: "num" }, `#${item.number}`),
      ),
      el("span", { class: "item-title" }, item.message || item.title),
      item.removable
        ? el(
            "button",
            {
              type: "button",
              class: "ghost",
              "aria-label": `Remove ${item.key} from the queue`,
              disabled: sending,
              onclick: () => send("/queue/remove", { key: item.key }),
            },
            "✕",
          )
        : null,
    );

  const queuePanel = () => {
    if (queue.items.length === 0) {
      return null;
    }
    const done = queue.items.filter((item) => item.finished).length;
    const failed = queue.items.filter((item) => item.step === "failed").length;
    const summary = [`${done} of ${queue.items.length} done`, failed > 0 ? `${failed} failed` : null]
      .filter(Boolean)
      .join(", ");
    return el(
      "div",
      { class: "queue" },
      el(
        "div",
        { class: "queue-head" },
        el(
          "button",
          { type: "button", class: "ghost toggle", "aria-expanded": String(queueOpen), onclick: () => {
            queueOpen = !queueOpen;
            render();
          } },
          el("strong", {}, "Queue"),
          " ",
          el("span", { class: "summary" }, summary),
        ),
        done > 0
          ? el("button", { type: "button", disabled: sending, onclick: () => send("/queue/clear", {}) }, "Clear finished")
          : null,
      ),
      queueOpen ? el("ol", {}, ...queue.items.map(queueItem)) : null,
    );
  };

  const render = () => {
    if (!island) {
      return;
    }
    const parts = [queuePanel(), selectionBar(), error ? el("p", { class: "island-error", role: "alert" }, error) : null]
      .filter(Boolean);
    island.replaceChildren(...parts);
    island.hidden = parts.length === 0;
    document.body.classList.toggle("has-island", parts.length > 0);
  };

  // --- Page refresh ------------------------------------------------------------------------------------------------

  // The server refreshes its data in the background, so fetching the page is cheap. Fetch quickly while the server
  // is fetching from GitHub, to pick up the result as soon as it is there.
  let refreshedAt = Date.now();
  let refreshing = false;

  const refreshPage = async () => {
    if (refreshing) {
      return;
    }
    refreshing = true;
    try {
      const response = await fetch(location.href, { cache: "no-store" });
      if (!response.ok) {
        return;
      }
      const next = new DOMParser().parseFromString(await response.text(), "text/html");
      for (const selector of ["header .meta", "main"]) {
        const current = document.querySelector(selector);
        const replacement = next.querySelector(selector);
        if (current && replacement) {
          current.replaceWith(document.adoptNode(replacement));
        }
      }
      document.body.dataset.polling = next.body.dataset.polling;
      syncSelection();
    } catch {
      // The server is gone or restarting; try again later.
    } finally {
      refreshedAt = Date.now();
      refreshing = false;
    }
  };

  setInterval(() => {
    const every = document.body.dataset.polling === "true" ? refreshQuickMs : refreshSlowMs;
    if (!document.hidden && Date.now() - refreshedAt >= every) {
      refreshPage();
    }
  }, 1000);

  syncSelection();
  pollQueue();
})();
