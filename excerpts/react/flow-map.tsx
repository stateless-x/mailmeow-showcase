import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, Check, CircleDashed, Globe, Inbox, KeyRound, RotateCcw, Send, Webhook, type LucideIcon } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'
import { api, type FlowMapData } from '@/lib/api'
import { joinPhrases } from '@/lib/join-phrases'
import { fill, useLang, useStrings } from '@/lib/lang'
import { qk } from '@/lib/query-keys'
import { formatRelative } from '@/lib/relative-time'
import { useCurrentOrg } from '@/lib/use-current-org'
import { cn } from '@/lib/utils'

export type FlowNodeId = 'app' | 'mailmeow' | 'domain' | 'inboxes' | 'webhook'
type State = 'done' | 'attention' | 'not_set'

const ICON: Record<FlowNodeId, LucideIcon> = { app: KeyRound, mailmeow: Send, domain: Globe, inboxes: Inbox, webhook: Webhook }
const STATE_ICON: Record<State, LucideIcon> = { done: Check, attention: AlertTriangle, not_set: CircleDashed }
const STATE_COLOR: Record<State, string> = { done: 'text-success', attention: 'text-warning', not_set: 'text-muted-foreground' }
const LINE: Record<State, string> = {
  done: 'border-solid border-foreground',
  attention: 'border-solid border-warning',
  not_set: 'border-dashed border-muted-foreground',
}

type NodeView = { id: FlowNodeId; state: State; detail: string }

function useNodes(flow: FlowMapData): NodeView[] {
  const f = useStrings().overview.flow
  const lang = useLang()
  const number = new Intl.NumberFormat(lang === 'th' ? 'th-TH' : 'en-GB')
  const n = (v: number) => number.format(v)
  const keys = flow.app.keys === 0 ? f.keys.none : flow.app.keys === 1 ? f.keys.one : fill(f.keys.many, { n: n(flow.app.keys) })
  // Under a minute the relative time would read "used now", which adds nothing, so it is left out.
  const justNow = flow.app.last_used_at ? Date.now() - new Date(flow.app.last_used_at).getTime() < 60_000 : false
  const used = flow.app.keys === 0 || justNow ? '' : flow.app.last_used_at ? fill(f.keys.used, { when: formatRelative(flow.app.last_used_at, lang) }) : f.keys.unused
  const inboxes = flow.inboxes
  const inboxLine = [
    inboxes.delivered + inboxes.bounced + inboxes.complained === 0 ? f.inboxes.none : fill(f.inboxes.counts, { delivered: n(inboxes.delivered), bounced: n(inboxes.bounced) }),
    inboxes.complained === 1 ? f.inboxes.spamOne : inboxes.complained > 1 ? fill(f.inboxes.spam, { n: n(inboxes.complained) }) : '',
  ]
  const hooks = flow.webhook.endpoints === 0 ? f.webhook.none : flow.webhook.endpoints === 1 ? f.webhook.one : fill(f.webhook.many, { n: n(flow.webhook.endpoints) })
  const join = (parts: string[]) => joinPhrases(parts, lang)
  return [
    { id: 'app', state: flow.app.state, detail: join([keys, used]) },
    { id: 'mailmeow', state: flow.mailmeow.state, detail: fill(f.mailmeow, { queued: n(flow.mailmeow.queued_today), sent: n(flow.mailmeow.sent_today) }) },
    { id: 'domain', state: flow.domain.state, detail: f.domain[flow.domain.state === 'done' ? 'done' : flow.domain.state === 'attention' ? 'attention' : 'notSet'] },
    { id: 'inboxes', state: flow.inboxes.state, detail: join(inboxLine) },
    { id: 'webhook', state: flow.webhook.state, detail: join([hooks, flow.webhook.failing_24h > 0 ? fill(f.webhook.failing, { n: n(flow.webhook.failing_24h) }) : '']) },
  ]
}

function NodeLink({ id, flow, orgId, className, children, label }: { id: FlowNodeId; flow: FlowMapData; orgId: string; className: string; children: ReactNode; label: string }) {
  const props = { className, 'aria-label': label, 'data-node': id, children }
  switch (id) {
    case 'app':
      return <Link to="/w/$orgId/keys" params={{ orgId }} {...props} />
    case 'domain':
      return <Link to="/w/$orgId/domains" params={{ orgId }} {...props} />
    case 'webhook':
      return <Link to="/w/$orgId/webhooks" params={{ orgId }} {...props} />
    default: {
      const status = id === 'inboxes' ? [...(flow.inboxes.bounced > 0 ? ['bounced' as const] : []), ...(flow.inboxes.complained > 0 ? ['complained' as const] : [])] : []
      return <Link to="/w/$orgId/emails" params={{ orgId }} search={status.length ? { status } : {}} {...props} />
    }
  }
}

/**
 * The whole workspace as one picture: Your app, mailmeow, Your domain, Inboxes, and the webhook that reports back.
 * Each node is a link to the screen that fixes it. A connector is solid once the node after it works and dashed until then.
 * Horizontal from 768px, vertical below. `compact` drops the count lines; `highlight` marks the node the page is about.
 */
export function FlowMap({ flow, compact = false, highlight }: { flow: FlowMapData; compact?: boolean; highlight?: FlowNodeId }) {
  const t = useStrings().overview
  const org = useCurrentOrg()
  const nodes = useNodes(flow)

  return (
    <section aria-label={t.flow.title} data-flow-map-root className="flex flex-col gap-2">
      <ol className={'flex list-none flex-col items-stretch md:flex-row'}>
        {nodes.map((node, i) => {
          const name = t.flow.node[node.id]
          const stateLabel = node.id === 'domain' && node.state === 'done' ? t.flow.state.ready : node.state === 'done' ? t.flow.state.done : node.state === 'attention' ? t.flow.state.attention : t.flow.state.notSet
          const StateIcon = STATE_ICON[node.state]
          const Icon = ICON[node.id]
          return (
            <Fragment key={node.id}>
              {i > 0 ? (
                <li aria-hidden data-connector={node.id} data-state={node.state} className="flex shrink-0 items-center justify-center self-center">
                  <span className={cn('block h-4 w-0 border-l-2 md:h-0 md:w-6 md:border-l-0 md:border-t-2', LINE[node.state])} />
                </li>
              ) : null}
              <li className="min-w-0 flex-1">
                <NodeLink
                  id={node.id}
                  flow={flow}
                  orgId={org.id}
                  label={fill(t.flow.label, { name, state: stateLabel, detail: compact ? '' : node.detail }).trim()}
                  className={cn(
                    'flex h-full flex-col gap-1 rounded-lg border bg-card p-3 text-left no-underline outline-none',
                    'hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring',
                    highlight === node.id ? 'border-2 border-primary bg-accent' : 'border-border',
                  )}
                >
                  <span className="flex items-center gap-(--icon-gap) font-semibold">
                    <Icon className="size-4 shrink-0" aria-hidden />
                    {name}
                  </span>
                  <span className={cn('flex items-center gap-(--icon-gap) text-sm font-medium', STATE_COLOR[node.state])}>
                    <StateIcon className="size-4 shrink-0" aria-hidden />
                    <span className="text-foreground">{stateLabel}</span>
                  </span>
                  {compact ? null : <span className="text-sm text-muted-foreground">{node.detail}</span>}
                </NodeLink>
              </li>
            </Fragment>
          )
        })}
      </ol>
      <p className="flex items-center gap-(--icon-gap) text-sm text-muted-foreground">
        <RotateCcw className="size-4 shrink-0" aria-hidden />
        {t.flow.back}
      </p>
    </section>
  )
}

/** For pages that have no overview of their own (Domains, Webhooks). It shows nothing until the numbers arrive. */
export function FlowMapFromOverview(props: { compact?: boolean; highlight?: FlowNodeId }) {
  const org = useCurrentOrg()
  const query = useQuery({ queryKey: qk.overview(org.id), queryFn: () => api.overview(org.id) })
  return query.data ? <FlowMap flow={query.data.flow} {...props} /> : null
}
