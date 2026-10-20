<script lang="ts">
  // Invite card for a mail that carries an iTIP (text/calendar) part.
  // Mounted by ConversationViewer when the calendar extension is enabled.
  // Answering stores the event in a calendar; when no provider tells the
  // organizer (InvitePreview's notifiesOrganizer), the card mails an iMIP
  // REPLY from the account whose address the invite lists.

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
  import { GetAttachments, GetAttachmentText, SendMessage, Calendar_PreviewInvite, Calendar_RespondToInvite, Calendar_BuildInviteReply } from '$wailsjs/go/app/App'
  import { smtp, type backend } from '$wailsjs/go/models'

  interface Props {
    messageId: string
  }

  let { messageId }: Props = $props()

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

  // The mail account the invite was sent to, if the user has one.
  const replyAccount = $derived.by(() => {
    const email = preview?.myEmail
    if (!email) return null
    return accountStore.accounts.find((aw) => (aw.account?.email || '').toLowerCase() === email)?.account ?? null
  })

  const serverNotifies = $derived.by(() => {
    if (!preview) return false
    if (preview.existing) return preview.existing.notifiesOrganizer
    return preview.targets.find((t) => t.calendarId === calendarId)?.notifiesOrganizer ?? false
  })

  const notifyLabel = $derived.by(() => {
    if (serverNotifies) return $_('calendar.invite.serverNotifies')
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

  async function mailReply(acc: { id: string; name: string; email: string }, partStat: string) {
    const reply = await Calendar_BuildInviteReply(ics, selfEmails, partStat)
    const answer = answerLabel(partStat)
    await SendMessage(acc.id, smtp.ComposeMessage.createFrom({
      from: { name: acc.name, address: reply.from },
      to: [{ name: reply.toName ?? '', address: reply.to }],
      cc: [],
      bcc: [],
      subject: $_('calendar.invite.replySubject', { values: { answer, summary: reply.summary } }),
      text_body: $_('calendar.invite.replyBody', { values: { name: acc.name || reply.from, answer, summary: reply.summary } }),
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
    if (!preview) return ''
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
        <div class="text-sm text-muted-foreground">
          {whenLabel}{#if preview.recurring} · {$_('calendar.invite.recurring')}{/if}
        </div>
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
      <p class="text-sm text-muted-foreground mt-3">{$_('calendar.invite.reply')}</p>
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
