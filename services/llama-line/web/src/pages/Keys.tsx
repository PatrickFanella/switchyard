import { useState } from 'react'
import { useKeys, useCreateKey, useRevokeKey, useUpdateKey } from '@/lib/api/hooks'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Table, THead, TBody, TR, TH, TD } from '@/components/ui/Table'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'
import { formatRelative } from '@/lib/utils'

export function KeysPage() {
  const { data } = useKeys()
  const create = useCreateKey()
  const revoke = useRevokeKey()
  const update = useUpdateKey()
  const [name, setName] = useState('')
  const [priority, setPriority] = useState(0)
  const [created, setCreated] = useState<string | null>(null)

  const onCreate = async () => {
    if (!name) return
    const res = await create.mutateAsync({ name, priority })
    setCreated(res.key)
    setName(''); setPriority(0)
  }

  return (
    <div className="grid gap-4 grid-cols-1 lg:grid-cols-3">
      <Card className="lg:col-span-1">
        <CardHeader><CardTitle>Create key</CardTitle></CardHeader>
        <CardContent className="space-y-2">
          <Input placeholder="name" value={name} onChange={e => setName(e.target.value)} className="w-full" />
          <Input type="number" placeholder="priority" value={priority} onChange={e => setPriority(parseInt(e.target.value || '0', 10))} className="w-full" />
          <Button onClick={onCreate} disabled={!name || create.isPending} className="w-full">
            {create.isPending ? 'Creating…' : 'Create'}
          </Button>
          {created && (
            <div className="mt-2 p-2 rounded bg-[var(--color-success)]/10 text-[var(--color-success)] text-xs break-all">
              new key (copy now, won't show again): <code className="font-mono">{created}</code>
              <Button size="sm" variant="ghost" className="ml-2" onClick={() => setCreated(null)}>dismiss</Button>
            </div>
          )}
        </CardContent>
      </Card>
      <Card className="lg:col-span-2">
        <CardHeader><CardTitle>Keys</CardTitle></CardHeader>
        <CardContent className="p-0">
          <Table>
            <THead><TR><TH>Name</TH><TH>Priority</TH><TH>Created</TH><TH></TH></TR></THead>
            <TBody>
              {(data?.keys ?? []).length === 0 ? (
                <TR><TD colSpan={4} className="text-[var(--color-muted)]">no keys</TD></TR>
              ) : data!.keys.map(k => (
                <TR key={k.name}>
                  <TD><Badge variant="info">{k.name}</Badge></TD>
                  <TD>
                    <Input type="number" defaultValue={k.priority}
                      onBlur={e => {
                        const p = parseInt(e.target.value, 10)
                        if (!Number.isNaN(p) && p !== k.priority) update.mutate({ name: k.name, priority: p })
                      }}
                      className="w-20 h-7"
                    />
                  </TD>
                  <TD>{formatRelative(k.created_at)}</TD>
                  <TD>
                    <Button size="sm" variant="destructive"
                      onClick={() => { if (confirm(`Revoke ${k.name}?`)) revoke.mutate(k.name) }}>
                      Revoke
                    </Button>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}
