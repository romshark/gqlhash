// Types for morpheus/min/bundle.js, which is imported for its side effect: it
// defines the kit's custom elements. Nothing is imported from it by name, so
// the module needs no exported types — what the page does with the components
// is set attributes and listen for the events declared below.

declare global {
  interface HTMLElementEventMap {
    /** Fired by <neo-tabs> when a different tab becomes the selected one. */
    "neo-tabs-change": CustomEvent<{ value: string }>;
    /** Fired by <neo-select> when an option is picked or the value cleared. */
    "neo-select-change": CustomEvent<{
      value: string | null;
      label: string | null;
    }>;
    /** Fired by <neo-checkbox> when the box is toggled by the user. */
    "neo-checkbox-change": CustomEvent<{
      checked: boolean;
      indeterminate: boolean;
    }>;
    /**
     * Fired by <neo-toggle-group> when a toggle in it is pressed. The group
     * holds a list, so value is comma-separated and values is it, split.
     */
    "neo-toggle-group-change": CustomEvent<{
      value: string;
      values: string[];
    }>;
  }
}

export {};
