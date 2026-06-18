import { useCallback, useEffect, useState } from 'react';
import type { ViewID } from '../types';

type NavigationState = {
  activeView: ViewID;
  selectedRunID: string;
};

const viewSet = new Set<ViewID>(['overview', 'queues', 'sessions', 'logs', 'prompts', 'config', 'agents', 'skills']);

function safeDecodeURIComponent(value: string) {
  try {
    return decodeURIComponent(value);
  } catch {
    return '';
  }
}

export function parseHash(hash: string | undefined): NavigationState {
  const normalized = hash?.trim() ?? '';

  if (!normalized.startsWith('#/')) {
    return { activeView: 'overview', selectedRunID: '' };
  }

  const parts = normalized.slice(2).split('/').filter(Boolean);
  const [viewPart, section, runID] = parts;

  if (viewPart === 'overview') {
    if (section === 'runs' && runID) {
      return { activeView: 'overview', selectedRunID: safeDecodeURIComponent(runID) };
    }

    return { activeView: 'overview', selectedRunID: '' };
  }

  if (viewPart && viewSet.has(viewPart as ViewID)) {
    return { activeView: viewPart as ViewID, selectedRunID: '' };
  }

  return { activeView: 'overview', selectedRunID: '' };
}

export function formatHash(activeView: ViewID, selectedRunID = '') {
  if (activeView === 'overview' && selectedRunID) {
    return `#/overview/runs/${encodeURIComponent(selectedRunID)}`;
  }

  return `#/${activeView}`;
}

export function useHashNavigation() {
  const [navigation, setNavigation] = useState<NavigationState>(() => parseHash(typeof window === 'undefined' ? '' : window.location.hash));

  useEffect(() => {
    if (typeof window === 'undefined') {
      return;
    }

    const syncFromHash = () => {
      setNavigation(parseHash(window.location.hash));
    };

    syncFromHash();
    window.addEventListener('hashchange', syncFromHash);

    return () => {
      window.removeEventListener('hashchange', syncFromHash);
    };
  }, []);

  const setActiveView = useCallback((activeView: ViewID) => {
    const next = { activeView, selectedRunID: activeView === 'overview' ? navigation.selectedRunID : '' };
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID);
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, [navigation.selectedRunID]);

  const setSelectedRunID = useCallback((selectedRunID: string) => {
    const next = { activeView: 'overview' as const, selectedRunID };
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID);
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  return {
    activeView: navigation.activeView,
    selectedRunID: navigation.selectedRunID,
    setActiveView,
    setSelectedRunID,
  };
}
