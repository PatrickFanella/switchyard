import { useCallback, useEffect, useState } from 'react';
import type { ViewID } from '../types';

type NavigationState = {
  activeView: ViewID;
  selectedRunID: string;
  selectedRequestID: string;
};

const viewSet = new Set<ViewID>(['overview', 'runs', 'queues', 'observability', 'sessions', 'logs', 'prompts', 'config', 'agents', 'skills']);

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
    return { activeView: 'overview', selectedRunID: '', selectedRequestID: '' };
  }

  const parts = normalized.slice(2).split('/').filter(Boolean);
  const [viewPart, section, runID] = parts;

  if (viewPart === 'overview') {
    if (section === 'runs' && runID) {
      return { activeView: 'overview', selectedRunID: safeDecodeURIComponent(runID), selectedRequestID: '' };
    }

    return { activeView: 'overview', selectedRunID: '', selectedRequestID: '' };
  }

  if (viewPart === 'runs') {
    if (section) {
      return { activeView: 'runs', selectedRunID: safeDecodeURIComponent(section), selectedRequestID: '' };
    }

    return { activeView: 'runs', selectedRunID: '', selectedRequestID: '' };
  }

  if (viewPart === 'queues') {
    if (section === 'requests' && runID) {
      return { activeView: 'queues', selectedRunID: '', selectedRequestID: safeDecodeURIComponent(runID) };
    }

    return { activeView: 'queues', selectedRunID: '', selectedRequestID: '' };
  }

  if (viewPart && viewSet.has(viewPart as ViewID)) {
    return { activeView: viewPart as ViewID, selectedRunID: '', selectedRequestID: '' };
  }

  return { activeView: 'overview', selectedRunID: '', selectedRequestID: '' };
}

export function formatHash(activeView: ViewID, selectedRunID = '', selectedRequestID = '') {
  if (activeView === 'runs' && selectedRunID) {
    return `#/runs/${encodeURIComponent(selectedRunID)}`;
  }

  if (activeView === 'runs') {
    return '#/overview';
  }

  if (activeView === 'overview' && selectedRunID) {
    return `#/overview/runs/${encodeURIComponent(selectedRunID)}`;
  }

  if (activeView === 'queues' && selectedRequestID) {
    return `#/queues/requests/${encodeURIComponent(selectedRequestID)}`;
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
    const next = { activeView, selectedRunID: activeView === 'overview' ? navigation.selectedRunID : '', selectedRequestID: activeView === 'queues' ? navigation.selectedRequestID : '' };
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, [navigation.selectedRequestID, navigation.selectedRunID]);

  const setSelectedRunID = useCallback((selectedRunID: string) => {
    const next = { activeView: 'overview' as const, selectedRunID, selectedRequestID: '' };
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  const openRunCockpit = useCallback((selectedRunID: string) => {
    const next = { activeView: 'runs' as const, selectedRunID, selectedRequestID: '' };
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  const setSelectedRequestID = useCallback((selectedRequestID: string) => {
    const next = { activeView: 'queues' as const, selectedRunID: '', selectedRequestID };
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  return {
    activeView: navigation.activeView,
    selectedRunID: navigation.selectedRunID,
    selectedRequestID: navigation.selectedRequestID,
    setActiveView,
    setSelectedRunID,
    openRunCockpit,
    setSelectedRequestID,
  };
}
