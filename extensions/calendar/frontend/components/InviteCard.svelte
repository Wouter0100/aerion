<script lang="ts">
  // Invite card for a mail that carries an iTIP (text/calendar) part.
  // Mounted by ConversationViewer when the calendar extension is enabled.
  // Answering stores the event in a calendar; when no provider tells the
  // organizer (InvitePreview's notifiesOrganizer), the card mails an iMIP
  // REPLY from the account that received the invite. When that is not the
  // invited address, the REPLY names it as SENT-BY for the invited one.

  import { _, locale } from 'svelte-i18n'
  import Icon from '@iconify/svelte'
  import { calendarSources } from '$extensions/calendar/frontend/stores/calendarSources.svelte'
  import { calendarSettings } from '$extensions/calendar/frontend/stores/calendarSettings.svelte'
  import { toTzDate } from '$extensions/calendar/frontend/lib/tzMath'
  import { collectSelfEmails } from '$extensions/calendar/frontend/lib/selfEmails'
  import { accountStore } from '$lib/stores/accounts.svelte'
  import { toasts } from '$lib/stores/toast'
  import RSVPControls from './RSVPControls.svelte'
  // @ts-ignore - wailsjs bindings
  import { GetAttachments, GetAttachmentText, GetIdentities, SendMessage, Calendar_PreviewInvite, Calendar_RespondToInvite, Calendar_BuildInviteReply } from '$wailsjs/go/app/App'
  import { smtp, type backend } from '$wailsjs/go/models'

  interface Props {
    messageId: string
    /** Account that received the message; the REPLY is mailed from it. */
    accountId: string
  }

  let { messageId, accountId }: Props = $props()

  let ics = $state('')
  let preview = $state<backend.InvitePreview | null>(null)
  let loadError = $state('')
  let calendarId = $state('')

  const selfEmails = $derived(collectSelfEmails(
    accountStore.accounts.map((aw) => aw.account?.email),
    calendarSources.sources,
  ))

  $effect(() => {
    void load(messageId)
  })

  async function load(id: string) {
    loadError = ''
    try {
      const atts = (await GetAttachments(id)) || []
      const att = atts.find((a) => (a.contentType || '').toLowerCase() === 'text/calendar')
      if (!att) {
        preview = null
        return
      }
      const text = await GetAttachmentText(att.id)
      if (calendarSources.sources.length === 0) {
        await calendarSources.load()
      }
      const p = await Calendar_PreviewInvite(text, selfEmails)
      if (id !== messageId) return
      ics = text
      preview = p
      if (!p.targets.some((t) => t.calendarId === calendarId)) {
        calendarId = p.targets.find((t) => t.notifiesOrganizer)?.calendarId ?? p.targets[0]?.calendarId ?? ''
      }
    } catch (err) {
      preview = null
      loadError = (err as Error)?.message ?? String(err)
    }
  }

  // The account that received the invite, else the one with the invited
  // address.
  const replyAccount = $derived.by(() => {
    const email = preview?.myEmail
    if (!email) return null
    const accounts = accountStore.accounts.map((aw) => aw.account).filter((a) => !!a)
    return accounts.find((a) => a.id === accountId)
      ?? accounts.find((a) => (a.email || '').toLowerCase() === email)
      ?? null
  })

  const serverNotifies = $derived.by(() => {
    if (!preview) return false
    if (preview.existing) return preview.existing.notifiesOrganizer
    return preview.targets.find((t) => t.calendarId === calendarId)?.notifiesOrganizer ?? false
  })

  const notifyLabel = $derived.by(() => {
    if (serverNotifies) return $_('calendar.invite.serverNotifies')
    if (replyAccount && (replyAccount.email || '').toLowerCase() !== preview?.myEmail) {
      return $_('calendar.invite.mailNotifiesOnBehalf', { values: { email: replyAccount.email, attendee: preview?.myEmail } })
    }
    if (replyAccount) return $_('calendar.invite.mailNotifies', { values: { email: replyAccount.email } })
    return $_('calendar.invite.noNotify')
  })

  function answerLabel(partStat: string): string {
    switch (partStat) {
      case 'ACCEPTED': return $_('calendar.attendees.partStatAccepted')
      case 'TENTATIVE': return $_('calendar.attendees.partStatTentative')
      case 'DECLINED': return $_('calendar.attendees.partStatDeclined')
    }
    return partStat
  }

  async function respond(partStat: string) {
    await Calendar_RespondToInvite(ics, calendarId, selfEmails, partStat)
    if (serverNotifies || !replyAccount) {
      toasts.success($_('calendar.invite.saved'))
    } else {
      try {
        await mailReply(replyAccount, partStat)
        toasts.success($_('calendar.invite.savedAndSent'))
      } catch (err) {
        const msg = (err as Error)?.message ?? String(err)
        toasts.error(`${$_('calendar.invite.sendFailed')}: ${msg}`)
      }
    }
    await load(messageId)
  }

  // The display name the composer would use for acc's address: its own
  // identity, else the account's default identity. Account.name is the
  // account's label, not a person's name.
  async function senderName(acc: { id: string; email: string }): Promise<string> {
    const identities = (await GetIdentities(acc.id)) || []
    const email = (acc.email || '').toLowerCase()
    const own = identities.filter((i) => (i.email || '').toLowerCase() === email)
    const identity = own.find((i) => i.isDefault) ?? own[0] ?? identities.find((i) => i.isDefault) ?? identities[0]
    return identity?.name || ''
  }

  async function mailReply(acc: { id: string; email: string }, partStat: string) {
    const reply = await Calendar_BuildInviteReply(ics, selfEmails, partStat, acc.email)
    const answer = answerLabel(partStat)
    const name = await senderName(acc)
    await SendMessage(acc.id, smtp.ComposeMessage.createFrom({
      from: { name, address: reply.from },
      to: [{ name: reply.toName ?? '', address: reply.to }],
      cc: [],
      bcc: [],
      subject: $_('calendar.invite.replySubject', { values: { answer, summary: reply.summary } }),
      text_body: $_('calendar.invite.replyBody', { values: { name: name || reply.from, answer, summary: reply.summary } }),
      html_body: '',
      attachments: [],
      calendar_method: 'REPLY',
      calendar_body: reply.ics,
    }))
  }

  // Same locale- and tz-aware formatting as EventDetail.
  const dateFmt = $derived(new Intl.DateTimeFormat($locale || undefined, {
    weekday: 'short', year: 'numeric', month: 'short', day: 'numeric',
    timeZone: calendarSettings.effectiveTimezone,
  }))
  const timeFmt = $derived(new Intl.DateTimeFormat($locale || undefined, {
    hour: '2-digit', minute: '2-digit',
    timeZone: calendarSettings.effectiveTimezone,
  }))

  const whenLabel = $derived.by(() => {
    if (!preview || !preview.dtstartUnix) return ''
    const start = new Date(preview.dtstartUnix * 1000)
    const end = new Date(preview.dtendUnix * 1000)
    if (preview.isAllDay) {
      return `${dateFmt.format(start)} (${$_('calendar.detail.allDay')})`
    }
    if (toTzDate(start).toDateString() === toTzDate(end).toDateString()) {
      return `${dateFmt.format(start)} · ${timeFmt.format(start)} – ${timeFmt.format(end)}`
    }
    return `${dateFmt.format(start)} ${timeFmt.format(start)} → ${dateFmt.format(end)} ${timeFmt.format(end)}`
  })
</script>

{#if preview}
  <div class="rounded-lg border border-border bg-muted/30 p-4 mt-4">
    <div class="flex items-start gap-3">
      <Icon icon="mdi:calendar-account" class="w-5 h-5 mt-0.5 text-primary flex-shrink-0" />
      <div class="min-w-0 flex-1 space-y-1">
        <div class="font-medium text-foreground">{preview.summary || $_('calendar.detail.noTitle')}</div>
        {#if whenLabel}
          <div class="text-sm text-muted-foreground">
            {whenLabel}{#if preview.recurring} · {$_('calendar.invite.recurring')}{/if}
          </div>
        {/if}
        {#if preview.location}
          <div class="text-sm text-muted-foreground break-words">{preview.location}</div>
        {/if}
        {#if preview.organizer}
          <div class="text-sm text-muted-foreground">
            {$_('calendar.attendees.organizer')}: {preview.organizer.cn || preview.organizer.email}
          </div>
        {/if}
      </div>
    </div>

    {#if preview.method === 'CANCEL'}
      <p class="text-sm text-foreground mt-3">{$_('calendar.invite.cancelled')}</p>
    {:else if preview.method === 'REPLY'}
      <p class="text-sm text-muted-foreground mt-3">
        {#if preview.replier}
          {$_('calendar.invite.replied', { values: { name: preview.replier.cn || preview.replier.email, answer: answerLabel(preview.replier.partStat || '') } })}
        {:else}
          {$_('calendar.invite.reply')}
        {/if}
      </p>
    {:else if !preview.myEmail}
      <p class="text-sm text-muted-foreground mt-3">{$_('calendar.invite.notInvited')}</p>
    {:else}
      <div class="mt-3 space-y-2">
        {#if preview.existing}
          <p class="text-sm text-muted-foreground">
            {$_('calendar.invite.inCalendar', { values: { calendar: preview.existing.calendarName } })}
          </p>
        {:else if preview.targets.length > 0}
          <label class="flex items-center gap-2 text-sm text-muted-foreground">
            <span class="flex-shrink-0">{$_('calendar.invite.addTo')}</span>
            <select
              class="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-1.5 text-sm text-foreground"
              bind:value={calendarId}
            >
              {#each preview.targets as target (target.calendarId)}
                <option value={target.calendarId}>{target.sourceName} · {target.calendarName}</option>
              {/each}
            </select>
          </label>
        {:else}
          <p class="text-sm text-muted-foreground">{$_('calendar.invite.noTarget')}</p>
        {/if}

        {#if preview.existing || preview.targets.length > 0}
          <RSVPControls
            {selfEmails}
            currentPartStat={preview.existing?.myPartStat ?? ''}
            {respond}
          />
          <p class="text-xs text-muted-foreground">{notifyLabel}</p>
        {/if}
      </div>
    {/if}
  </div>
{:else if loadError}
  <p class="text-xs text-muted-foreground mt-4">{$_('calendar.invite.loadFailed')}: {loadError}</p>
{/if}
