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
    const rebase = event.target.closest("[data-rebase]");
    if (rebase) {
      enqueue("rebase", [rebase.dataset.rebase]);
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
      } else if (tool.dataset.select === "behind") {
        select(all.filter((row) => row.hasAttribute("data-behind")).map((row) => row.dataset.key), true);
      } else if (tool.dataset.select === "failing") {
        select(all.filter((row) => row.dataset.ci === "bad").map((row) => row.dataset.key), true);
      }
    }
  });

  // --- Queue -------------------------------------------------------------------------------------------------------

  let queue = { items: [], busy: false, changes: 0 };
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

  const enqueue = async (action, keys = selectedKeys()) => {
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
      // Follow the new work closely, so that the table updates as soon as the approvals are in.
      scheduleQueuePoll(queueBusyMs);
    }
  };

  const setQueue = (next) => {
    const changed = next.changes !== queue.changes;
    queue = next;
    // Approving and merging change the pull requests, and the server is fetching them again by now. Fetching the page
    // picks up that the server is busy, which makes it refresh quickly until the new state is in.
    if (changed) {
      refreshPage();
    }
    render();
  };

  let queueTimer = null;

  // scheduleQueuePoll checks the queue after ms, replacing any check scheduled before.
  const scheduleQueuePoll = (ms) => {
    clearTimeout(queueTimer);
    queueTimer = setTimeout(pollQueue, ms);
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
    scheduleQueuePoll(queue.busy ? queueBusyMs : queueIdleMs);
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

  // linksReport lists the selected pull requests by CI result, in a form that can be handed to an AI agent. The CI
  // result is the class of the CI badge, see ciBadge in view.go. Groups without pull requests are left out.
  const linksReport = () => {
    const chosen = rows().filter((row) => selected.has(row.dataset.key));
    const groups = [
      ["PRs with failing CI:", (row) => row.dataset.ci === "bad"],
      ["PRs that pass CI:", (row) => row.dataset.ci === "good"],
      ["PRs with CI running or without checks:", (row) => row.dataset.ci !== "bad" && row.dataset.ci !== "good"],
    ];
    return groups
      .map(([heading, matches]) => [heading, ...chosen.filter(matches).map((row) => `* ${row.dataset.url}`)])
      .filter((lines) => lines.length > 1)
      .map((lines) => lines.join("\n"))
      .join("\n");
  };

  const copyText = async (text) => {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Not a secure context, or permission denied: fall back to the legacy way.
      const area = el("textarea", { style: "position:fixed;opacity:0" });
      area.value = text;
      document.body.append(area);
      area.select();
      try {
        return document.execCommand("copy");
      } finally {
        area.remove();
      }
    }
  };

  // --- Fix prompt --------------------------------------------------------------------------------------------------

  const promptKey = "fixPrompt";
  const linksPlaceholder = "{links}";
  const defaultPrompt = `These Dependabot pull requests fail CI. Fix them:

${linksPlaceholder}

For each pull request:
- Check out its branch and find out from the logs of the failing checks why CI fails.
- Make the smallest change that makes CI pass. Adapt the code to the updated dependency; do not pin, downgrade or revert it.
- Fold the fix into the Dependabot commit (amend it) instead of adding a commit, so that the pull request stays a single commit.
- Rebase the branch onto origin/master (or the repository's default branch, if it is not master), then force-push with --force-with-lease.
- If the failure is unrelated to the update, or fixing it needs a decision from me, leave the pull request as it is and tell me why.

Finish with a short summary of what you did to each pull request.`;

  // The template is a per-browser preference. Without storage the default is used and edits last for this page only.
  let promptTemplate = defaultPrompt;
  try {
    promptTemplate = localStorage.getItem(promptKey) ?? defaultPrompt;
  } catch {
    // Ignored: see above.
  }

  const savePromptTemplate = (template) => {
    promptTemplate = template;
    try {
      if (template === defaultPrompt) {
        localStorage.removeItem(promptKey);
      } else {
        localStorage.setItem(promptKey, template);
      }
    } catch {
      // Ignored: see above.
    }
  };

  // fixPrompt fills the template with the links of the selected pull requests, in the order the table shows them.
  const fixPrompt = () => {
    const links = rows()
      .filter((row) => selected.has(row.dataset.key))
      .map((row) => `- ${row.dataset.url}`)
      .join("\n");
    if (promptTemplate.includes(linksPlaceholder)) {
      return promptTemplate.replaceAll(linksPlaceholder, links);
    }
    return `${promptTemplate.trimEnd()}\n\n${links}`;
  };

  const promptDialog = document.querySelector(".prompt-dialog");
  const promptField = promptDialog?.querySelector("textarea");

  const editPrompt = () => {
    promptField.value = promptTemplate;
    promptDialog.returnValue = "";
    promptDialog.showModal();
  };

  // Saving on submit rather than on close, since close fires late, or not at all, in a page that is not shown.
  promptDialog?.querySelector("form").addEventListener("submit", (event) => {
    if (event.submitter?.value === "save") {
      savePromptTemplate(promptField.value.trim() === "" ? defaultPrompt : promptField.value);
    }
  });

  promptDialog?.querySelector("[data-prompt=reset]").addEventListener("click", () => {
    promptField.value = defaultPrompt;
    promptField.focus();
  });

  // --- Copying -----------------------------------------------------------------------------------------------------

  // copied holds the feedback shown on the copy button that was used last, until it times out.
  let copied = { button: "", label: "" };
  let copyTimer = null;

  const copy = async (button, text) => {
    copied = { button, label: (await copyText(text)) ? "Copied!" : "Copy failed" };
    render();
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => {
      copied = { button: "", label: "" };
      render();
    }, 2000);
  };

  const copyLabel = (button, label) => (copied.button === button ? copied.label : label);

  const selectionBar = () => {
    const count = selectedKeys().length;
    if (count === 0) {
      return null;
    }
    const behindKeys = rows()
      .filter((row) => selected.has(row.dataset.key) && row.hasAttribute("data-behind"))
      .map((row) => row.dataset.key);
    const hasBehind = behindKeys.length > 0;
    return el(
      "div",
      { class: "island-bar" },
      el("span", { class: "count" }, `${plural(count, "pull request")} selected`),
      el("span", { class: "actions" },
        el(
          "button",
          {
            type: "button",
            title: "Copy the links of the selected pull requests, grouped by CI result, to hand to an AI agent",
            onclick: () => copy("links", linksReport()),
          },
          copyLabel("links", "Copy links"),
        ),
        el(
          "span",
          { class: "split" },
          el(
            "button",
            {
              type: "button",
              title: "Copy a prompt asking an AI agent to fix CI of the selected pull requests",
              onclick: () => copy("prompt", fixPrompt()),
            },
            copyLabel("prompt", "Copy fix prompt"),
          ),
          promptDialog
            ? el("button", { type: "button", "aria-label": "Edit the fix prompt", title: "Edit the fix prompt", onclick: editPrompt }, "✎")
            : null,
        ),
        el(
          "button",
          {
            type: "button",
            disabled: sending || !hasBehind,
            title: hasBehind
              ? "Rebase the selected pull requests that are behind their base branch"
              : "None of the selected pull requests are behind their base branch",
            onclick: () => enqueue("rebase", behindKeys),
          },
          "Rebase",
        ),
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
      el(
        "span",
        { class: "item-title", title: item.message || item.title },
        item.message || item.title,
      ),
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
