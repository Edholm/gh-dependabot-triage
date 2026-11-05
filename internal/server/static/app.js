// Reload the page while it is visible, so the table stays current. The server refreshes its data in the background,
// so reloading is cheap. Reload quickly while the server is fetching, to pick up the result as soon as it is there.
(() => {
  const quickMs = 2 * 1000;
  const slowMs = 30 * 1000;
  const checkEveryMs = 1000;
  const reloadAfterMs = document.body.dataset.polling === "true" ? quickMs : slowMs;
  const loadedAt = Date.now();

  setInterval(() => {
    if (!document.hidden && Date.now() - loadedAt >= reloadAfterMs) {
      location.reload();
    }
  }, checkEveryMs);
})();
