(() => {
  'use strict'

  class HarnessAPI {
    constructor(onMuxFrame, onHostFrame, onConnection) {
      this.onMuxFrame = onMuxFrame
      this.onHostFrame = onHostFrame
      this.onConnection = onConnection
      this.sockets = []
      this.closed = false
    }

    async call(method, payload) {
      const rpcId = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
      const response = await fetch(`/api/${method}`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ type: 'client-request', rpcId, method, payload }),
      })
      if (!response.ok) throw new Error(`${method} 请求失败 (${response.status})`)
      const envelope = await response.json()
      if (envelope.rpcId !== rpcId) throw new Error(`${method} 返回了错误的 rpcId`)
      if (!envelope.result?.ok) throw new Error(envelope.result?.error?.message ?? `${method} 执行失败`)
      return envelope.result.value
    }

    connect() {
      this.openDownlink('/api/events.mux', this.onMuxFrame)
      this.openDownlink('/api/events.host', this.onHostFrame)
    }

    openDownlink(path, receive) {
      if (this.closed) return
      const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const socket = new WebSocket(`${protocol}//${location.host}${path}`)
      this.sockets.push(socket)
      socket.addEventListener('open', () => this.onConnection(true))
      socket.addEventListener('message', event => {
        try { receive(JSON.parse(event.data)) } catch (error) { console.error(error) }
      })
      socket.addEventListener('close', () => {
        this.onConnection(false)
        if (!this.closed) globalThis.setTimeout(() => this.openDownlink(path, receive), 900)
      })
      socket.addEventListener('error', () => socket.close())
    }

    close() {
      this.closed = true
      for (const socket of this.sockets) socket.close()
      this.sockets = []
    }
  }

  class ConversationApp {
    constructor() {
      this.sessionList = document.getElementById('session-list')
      this.messages = document.getElementById('messages')
      this.emptyState = document.getElementById('empty-state')
      this.title = document.getElementById('conversation-title')
      this.modelBadge = document.getElementById('model-badge')
      this.connectionLabel = document.getElementById('connection-label')
      this.stateDot = document.getElementById('state-dot')
      this.composer = document.getElementById('composer')
      this.prompt = document.getElementById('prompt')
      this.send = document.getElementById('send')
      this.composerState = document.getElementById('composer-state')
      this.toast = document.getElementById('toast')
      this.sessions = []
      this.events = new Map()
      this.streams = new Map()
      this.localTitles = new Map()
      this.currentSessionId = undefined
      this.host = undefined
      this.onlineDownlinks = 0
      this.api = new HarnessAPI(
        frame => this.receiveMux(frame),
        frame => this.receiveHost(frame),
        online => this.updateConnection(online),
      )
    }

    async start() {
      this.bindActions()
      this.setComposerEnabled(false)
      try {
        this.host = await this.api.call('host.describe', {})
        this.modelBadge.textContent = [this.host.provider, this.host.model].filter(Boolean).join(' / ') || 'DeepSeek'
        this.api.connect()
        await this.refreshSessions()
        if (this.sessions.length === 0) await this.createSession()
        else await this.selectSession(this.sessions[0].sessionId)
      } catch (error) {
        this.showError(error)
      }
    }

    bindActions() {
      document.getElementById('new-session').addEventListener('click', () => this.createSession())
      this.composer.addEventListener('submit', event => {
        event.preventDefault()
        this.submitPrompt()
      })
      this.prompt.addEventListener('keydown', event => {
        if (event.key === 'Enter' && !event.shiftKey) {
          event.preventDefault()
          this.composer.requestSubmit()
        }
      })
      this.prompt.addEventListener('input', () => this.resizePrompt())
      globalThis.addEventListener('beforeunload', () => this.api.close())
    }

    async refreshSessions() {
      const result = await this.api.call('session.list', {})
      this.sessions = [...(result.items ?? [])]
        .filter(item => item.origin !== 'subagent')
        .sort((left, right) => right.updatedAt - left.updatedAt)
      this.renderSessionList()
    }

    async createSession() {
      try {
        const payload = this.host?.cwd ? { cwd: this.host.cwd } : {}
        const result = await this.api.call('session.create', payload)
        await this.refreshSessions()
        await this.selectSession(result.sessionId)
        this.prompt.focus()
      } catch (error) {
        this.showError(error)
      }
    }

    async selectSession(sessionId) {
      this.currentSessionId = sessionId
      this.streams.delete(sessionId)
      this.renderSessionList()
      this.renderHeader()
      this.setComposerEnabled(false)
      try {
        const history = await this.api.call('session.history', { sessionId, maxMessages: 100 })
        this.events.set(sessionId, (history.events ?? []).map(entry => entry.event))
        this.renderMessages()
        this.setComposerEnabled(true)
        this.prompt.focus()
      } catch (error) {
        this.showError(error)
      }
    }

    async submitPrompt() {
      const text = this.prompt.value.trim()
      const sessionId = this.currentSessionId
      if (!text || !sessionId) return
      this.prompt.value = ''
      this.resizePrompt()
      this.localTitles.set(sessionId, text.slice(0, 52))
      this.renderSessionList()
      this.renderHeader()
      this.composerState.textContent = '已发送，Agent 正在处理'
      try {
        await this.api.call('session.prompt', {
          sessionId,
          mode: 'queue',
          content: [{ type: 'text', text }],
          clientTimeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
        })
      } catch (error) {
        this.composerState.textContent = '发送失败'
        this.showError(error)
      }
    }

    receiveMux(envelope) {
      const frame = envelope?.payload
      if (!frame || frame.type === 'session/subscribed') return
      if (frame.type === 'stream/error') {
        this.showError(new Error(frame.error?.message ?? '事件流已断开'))
        return
      }
      if (frame.type !== 'session/event') return
      const sessionEvents = this.events.get(frame.sessionId) ?? []
      if (!sessionEvents.some(event => event.seq === frame.event.seq)) {
        sessionEvents.push(frame.event)
        sessionEvents.sort((left, right) => left.seq - right.seq)
        this.events.set(frame.sessionId, sessionEvents)
      }
      this.applyStreamEvent(frame.sessionId, frame.event)
      if (frame.sessionId === this.currentSessionId) this.renderMessages()
      if (frame.event.type === 'turn/end') {
        this.composerState.textContent = '准备就绪'
        globalThis.setTimeout(() => this.refreshSessions().catch(error => this.showError(error)), 100)
      }
    }

    applyStreamEvent(sessionId, event) {
      if (event.type === 'assistant/chunk') {
        const chunk = event.data?.chunk
        const draft = this.streams.get(sessionId) ?? { text: '', reasoning: '' }
        if (chunk?.type === 'text-delta') draft.text += chunk.text ?? ''
        if (chunk?.type === 'reasoning-delta') draft.reasoning += chunk.text ?? ''
        this.streams.set(sessionId, draft)
      }
      if (event.type === 'assistant/message' || event.type === 'turn/end') this.streams.delete(sessionId)
    }

    receiveHost(envelope) {
      const frame = envelope?.payload
      if (!frame) return
      if (frame.type === 'host/session-added' || frame.type === 'host/session-removed') {
        this.refreshSessions().catch(error => this.showError(error))
        return
      }
      if (frame.type === 'host/session-status') {
        const session = this.sessions.find(item => item.sessionId === frame.sessionId)
        if (session) session.running = frame.running
        if (frame.sessionId === this.currentSessionId) {
          this.composerState.textContent = frame.running ? 'Agent 正在处理' : '准备就绪'
        }
        this.renderSessionList()
        return
      }
      if (frame.type === 'host/agent-error') this.showError(new Error(frame.message))
    }

    updateConnection(online) {
      this.onlineDownlinks = Math.max(0, this.onlineDownlinks + (online ? 1 : -1))
      const connected = this.onlineDownlinks > 0
      this.stateDot.classList.toggle('is-online', connected)
      this.connectionLabel.textContent = connected ? 'Go Agent 已连接' : '正在重新连接'
    }

    renderSessionList() {
      this.sessionList.replaceChildren()
      for (const session of this.sessions) {
        const button = document.createElement('button')
        button.type = 'button'
        button.className = `session-item${session.running ? ' is-running' : ''}`
        button.dataset.sessionId = session.sessionId
        button.setAttribute('aria-current', String(session.sessionId === this.currentSessionId))
        button.innerHTML = '<span class="session-title"></span><span class="session-meta"></span>'
        button.querySelector('.session-title').textContent = this.sessionTitle(session)
        button.querySelector('.session-meta').textContent = session.running ? 'RUNNING' : this.relativeTime(session.updatedAt)
        button.addEventListener('click', () => this.selectSession(session.sessionId))
        this.sessionList.append(button)
      }
    }

    renderHeader() {
      const session = this.sessions.find(item => item.sessionId === this.currentSessionId)
      this.title.textContent = session ? this.sessionTitle(session) : '新对话'
    }

    renderMessages() {
      const sessionEvents = this.events.get(this.currentSessionId) ?? []
      const rows = []
      for (const event of sessionEvents) {
        const message = this.messageFromEvent(event)
        if (message) rows.push(message)
      }
      const draft = this.streams.get(this.currentSessionId)
      if (draft && (draft.text || draft.reasoning)) rows.push({ role: 'assistant', ...draft, streaming: true })
      this.messages.replaceChildren()
      if (rows.length === 0) {
        this.messages.append(this.emptyState)
        return
      }
      for (const row of rows) this.messages.append(this.messageNode(row))
      this.messages.scrollTop = this.messages.scrollHeight
    }

    messageFromEvent(event) {
      if (event.type === 'user/message') return this.readMessage(event.data)
      if (event.type === 'assistant/message') return this.readMessage(event.data?.message)
      if (event.type === 'tool/result') return this.readMessage(event.data?.message)
      return undefined
    }

    readMessage(message) {
      if (!message?.content) return undefined
      let text = ''
      let reasoning = ''
      for (const block of message.content) {
        if (block.type === 'text') text += block.text ?? ''
        if (block.type === 'reasoning') reasoning += block.text ?? ''
        if (block.type === 'tool-result') text += this.flattenContent(block.content)
      }
      if (!text && !reasoning) return undefined
      return { role: message.role === 'assistant' ? 'assistant' : 'user', text, reasoning, streaming: false }
    }

    flattenContent(content) {
      return (content ?? []).filter(block => block.type === 'text').map(block => block.text ?? '').join('')
    }

    messageNode(message) {
      const article = document.createElement('article')
      article.className = `message ${message.role}${message.streaming ? ' streaming' : ''}`
      const role = document.createElement('div')
      role.className = 'message-role'
      role.textContent = message.role === 'assistant' ? 'Agent' : 'You'
      const body = document.createElement('div')
      body.className = 'message-body'
      if (message.reasoning) {
        const reasoning = document.createElement('p')
        reasoning.className = 'reasoning'
        reasoning.textContent = message.reasoning
        body.append(reasoning)
      }
      if (message.text) {
        const text = document.createElement('p')
        text.textContent = message.text
        body.append(text)
      }
      article.append(role, body)
      return article
    }

    sessionTitle(session) {
      const projected = session.projections?.values?.title
      return this.localTitles.get(session.sessionId) || projected || (session.blank ? '新对话' : `对话 ${session.sessionId.slice(0, 8)}`)
    }

    relativeTime(timestamp) {
      const elapsed = Math.max(0, Date.now() - timestamp)
      if (elapsed < 60_000) return '刚刚'
      if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)} 分钟前`
      if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)} 小时前`
      return new Date(timestamp).toLocaleDateString()
    }

    setComposerEnabled(enabled) {
      this.prompt.disabled = !enabled
      this.send.disabled = !enabled
    }

    resizePrompt() {
      this.prompt.style.height = 'auto'
      this.prompt.style.height = `${Math.min(this.prompt.scrollHeight, 180)}px`
    }

    showError(error) {
      const message = error instanceof Error ? error.message : String(error)
      this.toast.textContent = message
      this.toast.hidden = false
      globalThis.clearTimeout(this.toastTimer)
      this.toastTimer = globalThis.setTimeout(() => { this.toast.hidden = true }, 5000)
      console.error(error)
    }
  }

  const boot = () => new ConversationApp().start()
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot, { once: true })
  else boot()
})()
