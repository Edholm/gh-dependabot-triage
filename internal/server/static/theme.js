// Apply the saved theme and wire up the theme switcher in the footer. This script is loaded without defer, so the theme
// is set before the first paint. Otherwise the page would flash the wrong colors every time it reloads itself.
(() => {
  const storageKey = "theme";
  const system = "system";
  const themes = [system, "light", "dark"];
  const root = document.documentElement;

  // Storage can be unavailable (blocked cookies, private windows), in which case the theme only lasts for this page.
  const load = () => {
    try {
      const saved = localStorage.getItem(storageKey);
      return themes.includes(saved) ? saved : system;
    } catch {
      return system;
    }
  };

  const save = (theme) => {
    try {
      localStorage.setItem(storageKey, theme);
    } catch {
      // Ignored: see load.
    }
  };

  // Without a data-theme attribute the page follows the system theme.
  const apply = (theme) => {
    if (theme === system) {
      root.removeAttribute("data-theme");
    } else {
      root.dataset.theme = theme;
    }
  };

  apply(load());

  document.addEventListener("DOMContentLoaded", () => {
    const switcher = document.querySelector(".theme-switcher");
    if (!switcher) {
      return;
    }
    const buttons = switcher.querySelectorAll("button[data-theme]");
    const select = (theme) => {
      apply(theme);
      buttons.forEach((button) => button.setAttribute("aria-pressed", String(button.dataset.theme === theme)));
    };

    buttons.forEach((button) =>
      button.addEventListener("click", () => {
        save(button.dataset.theme);
        select(button.dataset.theme);
      }),
    );
    select(load());
    // The buttons do nothing without this script, so they stay hidden until it has run.
    switcher.hidden = false;
  });
})();
