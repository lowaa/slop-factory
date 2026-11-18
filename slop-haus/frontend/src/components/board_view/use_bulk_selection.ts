import { useCallback, useEffect, useState } from "react";

export interface BulkSelection<K> {
  active: boolean;
  selected: ReadonlySet<K>;
  enter: () => void;
  // Leaving select mode forgets the selection: coming back starts afresh.
  exit: () => void;
  toggle: (key: K) => void;
  selectAll: (keys: readonly K[]) => void;
  deselect: (keys: readonly K[]) => void;
}

interface State<K> {
  active: boolean;
  selected: ReadonlySet<K>;
}

// useBulkSelection is a select mode over a list of keys: which are picked,
// and whether picking is on at all. present is the keys the list has now;
// a key that leaves it is dropped from the selection (a ticket that left
// the board can't be acted on from it), and undefined means the list is
// not known right now -- loading -- so nothing is dropped.
//
// Esc leaves select mode, unless something inside it already handled the
// key (an open dropdown closing, which preventDefaults it).
export function useBulkSelection<K>(present: readonly K[] | undefined): BulkSelection<K> {
  const [state, setState] = useState<State<K>>(() => ({ active: false, selected: new Set<K>() }));

  // Pruned while rendering rather than in an effect, so the stale key is
  // never shown selected, not even for one frame. Once pruned the check
  // finds nothing more to drop, so this settles after one pass.
  if (present && [...state.selected].some((key) => !present.includes(key))) {
    const kept = new Set([...state.selected].filter((key) => present.includes(key)));
    setState({ ...state, selected: kept });
  }

  const enter = useCallback(() => setState((s) => ({ ...s, active: true })), []);
  const exit = useCallback(() => setState({ active: false, selected: new Set<K>() }), []);
  const toggle = useCallback(
    (key: K) =>
      setState((s) => {
        const selected = new Set(s.selected);
        if (!selected.delete(key)) selected.add(key);
        return { ...s, selected };
      }),
    [],
  );
  const selectAll = useCallback(
    (keys: readonly K[]) => setState((s) => ({ ...s, selected: new Set([...s.selected, ...keys]) })),
    [],
  );
  const deselect = useCallback(
    (keys: readonly K[]) =>
      setState((s) => ({ ...s, selected: new Set([...s.selected].filter((key) => !keys.includes(key))) })),
    [],
  );

  useEffect(() => {
    if (!state.active) return;
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape" && !e.defaultPrevented) exit();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [state.active, exit]);

  return { active: state.active, selected: state.selected, enter, exit, toggle, selectAll, deselect };
}
