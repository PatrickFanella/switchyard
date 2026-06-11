import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { LogStream } from '@/components/LogStream'

export function LogsPage() {
  return (
    <Card className="flex flex-col h-[calc(100vh-180px)]">
      <CardHeader><CardTitle>Logs (live)</CardTitle></CardHeader>
      <CardContent className="flex-1 flex flex-col p-3">
        <LogStream className="flex-1" initialCount={500} maxEntries={5000} />
      </CardContent>
    </Card>
  )
}
