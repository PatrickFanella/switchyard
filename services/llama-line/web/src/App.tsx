import { HashRouter, Routes, Route, Navigate } from 'react-router-dom'
import { Layout } from '@/components/Layout'
import { QueuePage } from '@/pages/Queue'
import { HistoryPage } from '@/pages/History'
import { LogsPage } from '@/pages/Logs'
import { AuditPage } from '@/pages/Audit'
import { KeysPage } from '@/pages/Keys'
import { ConfigPage } from '@/pages/Config'
import { StatsPage } from '@/pages/Stats'
import { SearchPage } from '@/pages/Search'

export function App() {
  return (
    <HashRouter>
      <Routes>
        <Route element={<Layout />}>
          <Route index element={<Navigate to="/queue" replace />} />
          <Route path="/queue"   element={<QueuePage />} />
          <Route path="/history" element={<HistoryPage />} />
          <Route path="/logs"    element={<LogsPage />} />
          <Route path="/audit"   element={<AuditPage />} />
          <Route path="/stats"   element={<StatsPage />} />
          <Route path="/search"  element={<SearchPage />} />
          <Route path="/keys"    element={<KeysPage />} />
          <Route path="/config"  element={<ConfigPage />} />
          <Route path="*"        element={<Navigate to="/queue" replace />} />
        </Route>
      </Routes>
    </HashRouter>
  )
}
