import { useCallback, useEffect, useRef, useState } from 'react';
import { confirmNavigation } from '../navigationGuard';
import type { ViewID } from '../types';

type NavigationState = {
  activeView: ViewID;
  selectedRunID: string;
  selectedRequestID: string;
};

const viewSet = new Set<ViewID>(['activity', 'overview', 'inbox', 'overseer', 'workflows', 'runs', 'queues', 'observability', 'sessions', 'logs', 'prompts', 'config', 'agents', 'skills']);

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
    return '#/runs';
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
  const activeViewRef = useRef(navigation.activeView);
  const lastHashRef = useRef(typeof window === 'undefined' ? '' : window.location.hash);
  activeViewRef.current = navigation.activeView;

  useEffect(() => {
    if (typeof window === 'undefined') {
      return;
    }

    const syncFromHash = () => {
      const next = parseHash(window.location.hash);
      // Browser back/forward to another view asks pages with unsaved edits first.
      if (next.activeView !== activeViewRef.current && !confirmNavigation()) {
        window.history.pushState(null, '', lastHashRef.current || '#/');
        return;
      }
      lastHashRef.current = window.location.hash;
      setNavigation(next);
    };

    syncFromHash();
    window.addEventListener('hashchange', syncFromHash);

    return () => {
      window.removeEventListener('hashchange', syncFromHash);
    };
  }, []);

  const setActiveView = useCallback((activeView: ViewID) => {
    if (activeView !== activeViewRef.current && !confirmNavigation()) {
      return;
    }
    const next = { activeView, selectedRunID: activeView === 'overview' ? navigation.selectedRunID : '', selectedRequestID: activeView === 'queues' ? navigation.selectedRequestID : '' };
    activeViewRef.current = next.activeView;
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      lastHashRef.current = hash;
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, [navigation.selectedRequestID, navigation.selectedRunID]);

  const setSelectedRunID = useCallback((selectedRunID: string) => {
    const next = { activeView: 'overview' as const, selectedRunID, selectedRequestID: '' };
    activeViewRef.current = next.activeView;
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      lastHashRef.current = hash;
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  const openRunCockpit = useCallback((selectedRunID: string) => {
    const next = { activeView: 'runs' as const, selectedRunID, selectedRequestID: '' };
    activeViewRef.current = next.activeView;
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      lastHashRef.current = hash;
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  const setSelectedRequestID = useCallback((selectedRequestID: string) => {
    const next = { activeView: 'queues' as const, selectedRunID: '', selectedRequestID };
    activeViewRef.current = next.activeView;
    setNavigation(next);

    if (typeof window !== 'undefined') {
      const hash = formatHash(next.activeView, next.selectedRunID, next.selectedRequestID);
      lastHashRef.current = hash;
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
