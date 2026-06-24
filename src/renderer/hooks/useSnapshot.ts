import { useEffect, useState } from 'react';
import type { AppSnapshot } from '../types';

/**
 * Subscribe to main-process snapshots.
 * Project pages ignore updates for other projects while keeping the project list fresh.
 */
export function useSnapshot(projectId: number | null) {
  const [snapshot, setSnapshot] = useState<AppSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let disposed = false;
    const showError = (e: unknown) => {
      const msg = e instanceof Error ? e.message : String(e);
      if (!disposed) setError(msg);
    };

    const unsubscribe = window.autoplan.onLoopUpdate((next) => {
      if (disposed) return;
      if (projectId === null || Number(next.activeProjectId) === Number(projectId)) {
        setSnapshot(next);
        return;
      }
      setSnapshot((current) => (current ? { ...current, projects: next.projects } : current));
    });

    window.autoplan
      .snapshot(projectId)
      .then((next) => {
        if (!disposed) setSnapshot(next);
      })
      .catch(showError);

    return () => {
      disposed = true;
      unsubscribe();
    };
  }, [projectId]);

  return { snapshot, setSnapshot, error, setError };
}
