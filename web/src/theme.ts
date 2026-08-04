// Which palette the page is in, and the switch that picks it.
//
// The choice is a class on <html> — `dark`, `light`, or neither before the
// first paint script runs — which is the convention Morpheus themes use too.
// The stylesheet holds the dark values under `:root.dark` rather than in a
// prefers-color-scheme query, because a query can't be overruled by a choice;
// what follows the system is this module, which resolves "system" itself and
// re-resolves it when the system changes underneath.
//
// The class is set by the inline script in index.html before anything paints.
// This module has to agree with it on the key and the class names.

const KEY = "gqlhash-theme";

export type Theme = "system" | "light" | "dark";

const system = window.matchMedia("(prefers-color-scheme: dark)");
const listeners = new Set<() => void>();

let chosen: Theme = read();

/** get returns the choice, which is "system" unless one was made. */
export function get(): Theme {
  return chosen;
}

/** dark reports which palette that choice comes out as right now. */
export function dark(): boolean {
  return chosen === "system" ? system.matches : chosen === "dark";
}

/** set records a choice, applies it and tells everyone watching. */
export function set(next: Theme): void {
  chosen = next;
  try {
    if (next === "system") {
      window.localStorage.removeItem(KEY);
    } else {
      window.localStorage.setItem(KEY, next);
    }
  } catch {
    // Disabled or full: the page still switches, it just won't remember.
  }
  paint();
}

/** clear forgets the choice, leaving the page on the system's palette. */
export function clear(): void {
  set("system");
}

/** onChange registers a callback for every switch, whoever made it. */
export function onChange(listener: () => void): void {
  listeners.add(listener);
}

/**
 * paint puts the resolved palette on <html> and tints the browser's own UI to
 * match. Both classes are written every time: a page that was dark and is now
 * light has to lose the one as well as gain the other.
 */
function paint(): void {
  const isDark = dark();
  document.documentElement.classList.toggle("dark", isDark);
  document.documentElement.classList.toggle("light", !isDark);
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute("content", isDark ? "#14121a" : "#f9f9fb");
  for (const listener of listeners) {
    listener();
  }
}

function read(): Theme {
  try {
    const saved = window.localStorage.getItem(KEY);
    return saved === "light" || saved === "dark" ? saved : "system";
  } catch {
    return "system";
  }
}

// The system moving while the page is on "system" is a switch like any other.
system.addEventListener("change", () => {
  if (chosen === "system") {
    paint();
  }
});

/** The three choices, in the order they're offered. */
const choices: readonly {
  readonly theme: Theme;
  readonly label: string;
  readonly icon?: string;
  readonly text?: string;
}[] = [
  { theme: "light", label: "Light theme", icon: "sun" },
  { theme: "dark", label: "Dark theme", icon: "moon" },
  { theme: "system", label: "Follow the system", text: "Auto" },
];

/**
 * mount builds a switch in host and keeps it in step with the choice, whether
 * it was made here or in the other one. <neo-toggle-group> holds a list of
 * values rather than a single one, so what a click leaves behind is trimmed
 * back to the one that was pressed.
 */
export function mount(host: HTMLElement): void {
  const group = document.createElement("neo-toggle-group");
  group.className = "theme-switch";
  group.setAttribute("aria-label", "Theme");

  for (const choice of choices) {
    const toggle = document.createElement("neo-toggle");
    toggle.setAttribute("value", choice.theme);
    toggle.setAttribute("aria-label", choice.label);
    toggle.title = choice.label;
    if (choice.icon) {
      const icon = document.createElement("neo-icon");
      icon.setAttribute("name", choice.icon);
      toggle.append(icon);
    }
    if (choice.text) {
      toggle.append(choice.text);
    }
    group.append(toggle);
  }

  group.addEventListener("neo-toggle-group-change", (event) => {
    const values = event.detail.value.split(",").filter(Boolean);
    // The one the click added is the one the user meant; pressing the
    // already-pressed one leaves nothing behind and changes nothing.
    const picked = values.find((value) => value !== chosen) ?? chosen;
    set(picked === "light" || picked === "dark" ? picked : "system");
  });

  const show = () => group.setAttribute("value", chosen);
  onChange(show);
  show();

  host.replaceChildren(group);
}
