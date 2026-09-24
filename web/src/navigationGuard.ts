// A page with unsaved edits registers a guard; shell navigation (sidebar
// clicks and browser back/forward between views) asks every guard before it
// leaves. A guard returns true when leaving is allowed.
export type NavigationGuard = () => boolean;

const guards = new Set<NavigationGuard>();

export function registerNavigationGuard(guard: NavigationGuard) {
  guards.add(guard);
  return () => {
    guards.delete(guard);
  };
}

export function confirmNavigation() {
  for (const guard of guards) {
    if (!guard()) return false;
  }
  return true;
}

export const unsavedChangesMessage = 'You have unsaved changes to this workflow draft. Leave and discard them?';
