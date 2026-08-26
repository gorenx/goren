import { createContext, useContext, useEffect, useMemo, useState } from 'react'

const simplifiedChinese = {
  'app.dismissError': '关闭错误提示',
  'sidebar.navigation': '会话导航',
  'sidebar.newConversation': '新对话',
  'sidebar.creatingConversation': '正在创建新对话',
  'sidebar.expand': '展开侧栏',
  'sidebar.collapse': '收起侧栏',
  'sidebar.recentSessions': '最近会话',
  'sidebar.conversationList': '对话列表',
  'sidebar.loadMoreSessions': '加载更多',
  'sidebar.loadingSessions': '正在加载',
  'sidebar.hostConnected': 'Host 已连接',
  'sidebar.partiallyConnected': '部分连接',
  'sidebar.reconnecting': '正在重新连接',
  'sidebar.booting': '启动中',
  'sidebar.running': '运行中',
  'conversation.session': '会话',
  'conversation.new': '新对话',
  'conversation.newID': '新建',
  'conversation.configureCredential': '设置 DeepSeek API Key',
  'conversation.runtimeStatus': '当前运行状态',
  'conversation.live': '在线',
  'conversation.partial': '部分连接',
  'conversation.connecting': '连接中',
  'conversation.running': '运行中',
  'conversation.idle': '空闲',
  'conversation.durable': '已持久化 · {sequence}',
  'conversation.events': '{count} 个事件',
  'conversation.content': '对话内容',
  'conversation.loadOlder': '加载更早消息',
  'conversation.loadingOlder': '正在加载…',
  'conversation.emptyTitle': '开始一段可恢复的对话',
  'conversation.emptyDescription': '每个回复都经过 Go Agent Loop，并以 Session facts 保留上下文。',
  'conversation.currentWorkspace': '当前工作区',
  'message.youMark': '你',
  'message.you': '你',
  'message.streaming': '生成中',
  'message.reasoning': '思考过程',
  'composer.label': '发送消息',
  'composer.waitingPlaceholder': '请先回答上方问题…',
  'composer.placeholder': '给 Agent 发消息…',
  'composer.enterToSend': '按 Enter 发送',
  'composer.send': '发送消息',
  'composer.stop': '终止当前 Agent 推理',
  'composer.connecting': '正在连接 Go Agent',
  'composer.syncingSessions': '正在同步会话',
  'composer.creatingSession': '正在创建会话',
  'composer.readingHistory': '正在读取历史',
  'composer.agentWorking': 'Agent 正在处理',
  'composer.cancelling': '正在终止 Agent 推理',
  'composer.cancelRequested': '已请求终止 Agent 推理',
  'composer.ready': '准备就绪',
  'composer.queued': '已进入 Agent 队列',
  'composer.sendFailed': '发送失败',
  'composer.waitingForAnswer': 'Agent 正在等待你的回答',
  'composer.agentContinuing': 'Agent 正在继续处理',
  'composer.questionCancelled': '已取消问题，Agent 正在收尾',
  'composer.factsSynced': '事实已同步，准备就绪',
  'details.label': '会话详情',
  'details.context': '上下文',
  'details.currentSession': '当前会话',
  'details.executionStatus': '执行状态',
  'details.agent': 'Agent',
  'details.factBoundary': '事实边界',
  'details.noEvents': '暂无事件',
  'details.workspace': '工作区',
  'details.notSet': '未设置',
  'details.modelRoute': '模型路由',
  'details.configured': '已配置 · {source}',
  'details.notConfigured': '未配置',
  'details.noSession': '尚未选择',
  'details.mainFlow': '主流程',
  'details.browser': '浏览器',
  'details.note': '这里只展示 Host 已提供的事实。完整 Settings、插件清单和文件系统能力没有在浏览器中伪造。',
  'credential.description': 'Key 只写入 Agent 的私有凭据文件，浏览器不会回读明文。',
  'credential.close': '关闭',
  'credential.currentStatus': '当前状态',
  'credential.environmentReadOnly': '当前 Key 由启动环境变量提供，Web 不能覆盖。请修改启动 Agent 的',
  'credential.environmentSuffix': '。',
  'credential.newKey': '新的 API Key',
  'credential.replacePlaceholder': '输入新 Key 以替换',
  'credential.remove': '删除已存 Key',
  'credential.later': '稍后设置',
  'credential.saving': '保存中…',
  'credential.save': '保存 Key',
  'credential.required': '请输入 API Key',
  'credential.invalid': '只粘贴 API Key，不要包含变量名、引号或空格',
  'question.label': 'Agent 正在等待回答',
  'question.eyebrow': 'AGENT 需要输入',
  'question.title': '回答后继续当前任务',
  'question.waiting': '等待中',
  'question.fallbackHeader': '问题 {number}',
  'question.yourAnswer': '你的回答',
  'question.otherAnswer': '其他回答',
  'question.answerPlaceholder': '输入回答…',
  'question.otherPlaceholder': '也可以直接输入…',
  'question.answerAll': '请回答每一个问题',
  'question.cancel': '取消提问',
  'question.continuing': '正在继续…',
  'question.submit': '回答并继续',
  'language.label': '界面语言',
  'language.chinese': '中文',
  'language.english': 'English',
  'session.fallback': '对话 {id}',
  'time.justNow': '刚刚',
  'time.minutesAgo': '{count} 分钟前',
  'time.hoursAgo': '{count} 小时前',
  'error.questionEnded': '这个问题已经结束',
  'error.streamDisconnected': '事件流已断开',
  'error.invalidQuestion': 'Host 返回了无效的问题请求',
  'error.agentFailed': 'Agent 执行失败',
  'error.invalidRPCEnvelope': 'Host 返回了无效的 RPC envelope',
  'error.invalidRPCResult': 'Host 返回了无效的 RPC result',
  'error.invalidServerRequest': 'Host 返回了无效的 server-request',
  'error.requestFailed': '{method} 请求失败 ({status})',
  'error.wrongRPCID': '{method} 返回了错误的 rpcId',
  'error.methodFailed': '{method} 执行失败',
  'error.respondFailed': '回答 Host 请求失败 ({status})',
  'error.invalidReceipt': 'Host 返回了无效的回答回执',
  'error.responseReason': '：{reason}',
  'error.responseRejected': 'Host 未接受回答{reason}',
} as const

export type MessageKey = keyof typeof simplifiedChinese
export type Locale = 'zh-CN' | 'en'
export type TranslationValues = Readonly<Record<string, string | number>>
export type Translator = (messageKey: MessageKey, values?: TranslationValues) => string

const english = {
  'app.dismissError': 'Dismiss error',
  'sidebar.navigation': 'Conversation navigation',
  'sidebar.newConversation': 'New conversation',
  'sidebar.creatingConversation': 'Creating conversation',
  'sidebar.expand': 'Expand sidebar',
  'sidebar.collapse': 'Collapse sidebar',
  'sidebar.recentSessions': 'Recent sessions',
  'sidebar.conversationList': 'Conversation list',
  'sidebar.loadMoreSessions': 'Load more',
  'sidebar.loadingSessions': 'Loading',
  'sidebar.hostConnected': 'Host connected',
  'sidebar.partiallyConnected': 'Partially connected',
  'sidebar.reconnecting': 'Reconnecting',
  'sidebar.booting': 'Booting',
  'sidebar.running': 'Running',
  'conversation.session': 'Session',
  'conversation.new': 'New conversation',
  'conversation.newID': 'New',
  'conversation.configureCredential': 'Configure DeepSeek API Key',
  'conversation.runtimeStatus': 'Current runtime status',
  'conversation.live': 'Live',
  'conversation.partial': 'Partial',
  'conversation.connecting': 'Connecting',
  'conversation.running': 'Running',
  'conversation.idle': 'Idle',
  'conversation.durable': 'Durable · {sequence}',
  'conversation.events': '{count} events',
  'conversation.content': 'Conversation content',
  'conversation.loadOlder': 'Load earlier messages',
  'conversation.loadingOlder': 'Loading…',
  'conversation.emptyTitle': 'Start a recoverable conversation',
  'conversation.emptyDescription': 'Every response runs through the Go Agent Loop, with context preserved as Session facts.',
  'conversation.currentWorkspace': 'Current workspace',
  'message.youMark': 'You',
  'message.you': 'You',
  'message.streaming': 'Streaming',
  'message.reasoning': 'Reasoning',
  'composer.label': 'Send a message',
  'composer.waitingPlaceholder': 'Answer the question above first…',
  'composer.placeholder': 'Message the Agent…',
  'composer.enterToSend': 'Enter to send',
  'composer.send': 'Send message',
  'composer.stop': 'Stop the current Agent turn',
  'composer.connecting': 'Connecting to Go Agent',
  'composer.syncingSessions': 'Syncing sessions',
  'composer.creatingSession': 'Creating session',
  'composer.readingHistory': 'Loading history',
  'composer.agentWorking': 'Agent is working',
  'composer.cancelling': 'Stopping the Agent turn',
  'composer.cancelRequested': 'Agent stop requested',
  'composer.ready': 'Ready',
  'composer.queued': 'Queued for the Agent',
  'composer.sendFailed': 'Send failed',
  'composer.waitingForAnswer': 'Agent is waiting for your answer',
  'composer.agentContinuing': 'Agent is continuing',
  'composer.questionCancelled': 'Question cancelled; Agent is wrapping up',
  'composer.factsSynced': 'Facts synced; ready',
  'details.label': 'Conversation details',
  'details.context': 'Context',
  'details.currentSession': 'Current session',
  'details.executionStatus': 'Execution status',
  'details.agent': 'Agent',
  'details.factBoundary': 'Fact boundary',
  'details.noEvents': 'No events yet',
  'details.workspace': 'Workspace',
  'details.notSet': 'Not set',
  'details.modelRoute': 'Model route',
  'details.configured': 'Configured · {source}',
  'details.notConfigured': 'Not configured',
  'details.noSession': 'No session selected',
  'details.mainFlow': 'Main flow',
  'details.browser': 'Browser',
  'details.note': 'Only facts provided by the Host appear here. The browser does not invent full Settings, plugin inventory, or filesystem capabilities.',
  'credential.description': 'The Key is written only to the Agent private credential file. The browser never reads the plaintext back.',
  'credential.close': 'Close',
  'credential.currentStatus': 'Current status',
  'credential.environmentReadOnly': 'The current Key comes from the startup environment and cannot be replaced in the Web UI. Update this variable before starting the Agent:',
  'credential.environmentSuffix': '.',
  'credential.newKey': 'New API Key',
  'credential.replacePlaceholder': 'Enter a new Key to replace it',
  'credential.remove': 'Remove saved Key',
  'credential.later': 'Set up later',
  'credential.saving': 'Saving…',
  'credential.save': 'Save Key',
  'credential.required': 'Enter an API Key',
  'credential.invalid': 'Paste only the API Key, without a variable name, quotes, or spaces',
  'question.label': 'Agent is waiting for an answer',
  'question.eyebrow': 'Agent needs input',
  'question.title': 'Answer to continue the current task',
  'question.waiting': 'Waiting',
  'question.fallbackHeader': 'Question {number}',
  'question.yourAnswer': 'Your answer',
  'question.otherAnswer': 'Other answer',
  'question.answerPlaceholder': 'Enter your answer…',
  'question.otherPlaceholder': 'Or enter a custom answer…',
  'question.answerAll': 'Answer every question',
  'question.cancel': 'Cancel question',
  'question.continuing': 'Continuing…',
  'question.submit': 'Answer and continue',
  'language.label': 'Interface language',
  'language.chinese': '中文',
  'language.english': 'English',
  'session.fallback': 'Conversation {id}',
  'time.justNow': 'Just now',
  'time.minutesAgo': '{count} min ago',
  'time.hoursAgo': '{count} hr ago',
  'error.questionEnded': 'This question has already ended',
  'error.streamDisconnected': 'The event stream disconnected',
  'error.invalidQuestion': 'Host returned an invalid question request',
  'error.agentFailed': 'Agent execution failed',
  'error.invalidRPCEnvelope': 'Host returned an invalid RPC envelope',
  'error.invalidRPCResult': 'Host returned an invalid RPC result',
  'error.invalidServerRequest': 'Host returned an invalid server-request',
  'error.requestFailed': '{method} request failed ({status})',
  'error.wrongRPCID': '{method} returned the wrong rpcId',
  'error.methodFailed': '{method} failed',
  'error.respondFailed': 'Responding to the Host request failed ({status})',
  'error.invalidReceipt': 'Host returned an invalid response receipt',
  'error.responseReason': ': {reason}',
  'error.responseRejected': 'Host rejected the response{reason}',
} satisfies Record<MessageKey, string>

const resources: Record<Locale, Record<MessageKey, string>> = {
  'zh-CN': simplifiedChinese,
  en: english,
}
const storageKey = 'goren.locale'

interface I18nValue {
  activeLanguage: Locale
  changeLanguage: (nextLanguage: Locale) => void
  translate: Translator
}

const I18nContext = createContext<I18nValue | undefined>(undefined)

export function I18nProvider({ children }: { children: React.ReactNode }): React.JSX.Element {
  const [activeLanguage, setActiveLanguage] = useState<Locale>(resolveInitialLanguage)

  useEffect(() => {
    document.documentElement.lang = activeLanguage
  }, [activeLanguage])

  const contextValue = useMemo<I18nValue>(() => ({
    activeLanguage,
    changeLanguage: nextLanguage => {
      setActiveLanguage(nextLanguage)
      try {
        globalThis.localStorage?.setItem(storageKey, nextLanguage)
      } catch {
        // Language selection still applies when storage is unavailable.
      }
    },
    translate: (messageKey, values) => renderMessage(activeLanguage, messageKey, values),
  }), [activeLanguage])

  return <I18nContext.Provider value={contextValue}>{children}</I18nContext.Provider>
}

export function useI18n(): I18nValue {
  const contextValue = useContext(I18nContext)
  if (contextValue === undefined) throw new Error('web: missing I18nProvider')
  return contextValue
}

export function isLocale(value: string): value is Locale {
  return value === 'zh-CN' || value === 'en'
}

function renderMessage(activeLanguage: Locale, messageKey: MessageKey, values?: TranslationValues): string {
  const template = resources[activeLanguage][messageKey]
  if (values === undefined) return template
  return template.replace(/\{([^}]+)\}/g, (placeholder, name: string) => {
    const replacement = values[name]
    return replacement === undefined ? placeholder : String(replacement)
  })
}

function resolveInitialLanguage(): Locale {
  try {
    const stored = globalThis.localStorage?.getItem(storageKey)
    if (stored !== null && stored !== undefined && isLocale(stored)) return stored
  } catch {
    // Fall through to browser language detection.
  }
  const preferredLanguage = globalThis.navigator?.languages?.[0] ?? globalThis.navigator?.language ?? 'en'
  return preferredLanguage.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}
