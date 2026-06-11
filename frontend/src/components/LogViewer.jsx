import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import AlertBanner from './AlertBanner.jsx'

const API_BASE = '/api'
const MAX_LINES = 10000
const MAX_BUFFER = 5000


// ── TICKET-020: Severity detection ──
const SEVERITY_PATTERNS = [
  { level: 'fatal',   regex: /\b(FATAL|FATALITY)\b/i,  label: 'FATAL', color: 'bg-fuchsia-600 text-white' },
  { level: 'panic',   regex: /\bPANIC\b/i,              label: 'PANIC', color: 'bg-purple-600 text-white' },
  { level: 'error',   regex: /\bERROR\b/i,              label: 'ERROR', color: 'bg-red-600 text-white' },
  { level: 'warn',    regex: /\bWARN(?:ING)?\b/i,       label: 'WARN',  color: 'bg-amber-500 text-white' },
]

const ERROR_LIKE_LEVELS = new Set(['error', 'fatal', 'panic'])

/**
 * Detect severity level from a log message.
 * Returns { level, label, color } or null if no severity pattern matches.
 */
function detectSeverity(message) {
  for (const pattern of SEVERITY_PATTERNS) {
    if (pattern.regex.test(message)) {
      return { level: pattern.level, label: pattern.label, color: pattern.color }
    }
  }
  return null
}

// ── TICKET-021: Rolling-window alert tracker ──
// Tracks timestamps of ERROR-like entries to detect >N in 1 minute.
class AlertTracker {
  constructor(threshold = 20, windowMs = 60_000) {
    this.threshold = threshold
    this.windowMs = windowMs
    this.timestamps = [] // sorted array of epoch ms for error-like log entries
  }

  /** Record a new error-like entry. Returns the current count within the window. */
  record(nowMs = Date.now()) {
    this.timestamps.push(nowMs)
    this._prune(nowMs)
    return this.timestamps.length
  }

  /** Get the current count of error-like entries in the rolling window. */
  getCount(nowMs = Date.now()) {
    this._prune(nowMs)
    return this.timestamps.length
  }

  /** Check if the threshold is exceeded. */
  isExceeded(nowMs = Date.now()) {
    return this.getCount(nowMs) >= this.threshold
  }

  /** Prune timestamps outside the window. */
  _prune(nowMs) {
    const cutoff = nowMs - this.windowMs
    // Timestamps are sorted — find first index >= cutoff
    let i = 0
    while (i < this.timestamps.length && this.timestamps[i] < cutoff) {
      i++
    }
    if (i > 0) {
      this.timestamps = this.timestamps.slice(i)
    }
  }

  reset() {
    this.timestamps = []
  }
}

// ── Helpers ──
let logIdCounter = 0

function formatTime(ts) {
  try {
    const d = new Date(ts)
    return d.toLocaleTimeString('en-US', { hour12: false })
  } catch {
    return ''
  }
}

// ── Alert severity level (derived from count vs threshold) ──
function alertLevel(count) {
  if (count >= 100) return 'critical'
  if (count >= 50) return 'error'
  return 'warn'
}

export default function LogViewer({ containerId, containerName }) {
  // ── Log state ──
  const [logs, setLogs] = useState([])
  const [isPaused, setIsPaused] = useState(false)
  const [pausedCount, setPausedCount] = useState(0)

  // ── WebSocket state ──
  const [wsStatus, setWsStatus] = useState('idle')

  // ── Search state ──
  const [searchQuery, setSearchQuery] = useState('')
  const [serverResults, setServerResults] = useState(null)
  const [searchLoading, setSearchLoading] = useState(false)
  const [showServerSearch, setShowServerSearch] = useState(false)

  // ── TICKET-020: Severity filter state ──
  const [severityFilter, setSeverityFilter] = useState(null) // null = all, 'error' = error-like only, etc.

  // ── TICKET-021: Alert state ──
  const [activeAlerts, setActiveAlerts] = useState([])
  const [errorCount, setErrorCount] = useState(0) // rolling window count

  // ── Scroll state ──
  const [userScrolledUp, setUserScrolledUp] = useState(false)

  // ── Refs ──
  const wsRef = useRef(null)
  const logContainerRef = useRef(null)
  const pausedBufferRef = useRef([])
  const partialLineRef = useRef('')
  const reconnectTimerRef = useRef(null)
  const mountedRef = useRef(true)
  const isPausedRef = useRef(false)
  const alertTrackerRef = useRef(new AlertTracker(20, 60_000))
  const alertCheckTimerRef = useRef(null)

  // Keep isPausedRef in sync
  useEffect(() => {
    isPausedRef.current = isPaused
  }, [isPaused])

  // ── TICKET-021: Periodic alert check ──
  // Run a timer that checks the rolling window and fires/clears alerts
  useEffect(() => {
    const checkAlerts = () => {
      const tracker = alertTrackerRef.current
      const count = tracker.getCount()
      setErrorCount(count)

      if (tracker.isExceeded()) {
        // Add or update the "error burst" alert
        setActiveAlerts((prev) => {
          const existingIdx = prev.findIndex((a) => a.type === 'error_burst')
          const newAlert = {
            id: 'error_burst',
            type: 'error_burst',
            level: alertLevel(count),
            message: `More than ${tracker.threshold} ERROR logs in 1 minute — possible incident in progress`,
            count,
            time: new Date().toLocaleTimeString('en-US', { hour12: false }),
          }
          if (existingIdx >= 0) {
            const updated = [...prev]
            updated[existingIdx] = newAlert
            return updated
          }
          return [newAlert, ...prev]
        })
      } else {
        // Remove error_burst alert if it exists and count is below threshold
        setActiveAlerts((prev) => prev.filter((a) => a.type !== 'error_burst'))
      }
    }

    // Check every 2 seconds
    alertCheckTimerRef.current = setInterval(checkAlerts, 2000)

    return () => {
      if (alertCheckTimerRef.current) {
        clearInterval(alertCheckTimerRef.current)
      }
    }
  }, [])

  // ── Build WebSocket URL ──
  const wsUrl = useMemo(() => {
    if (!containerId) return null
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    return `${protocol}//${window.location.host}/api/containers/${containerId}/logs/live?tail=100`
  }, [containerId])

  // ── Process incoming log entries ──
  const processLogEntries = useCallback((entries) => {
    // TICKET-020 & TICKET-021: Process each entry for severity + alert tracking
    const tracker = alertTrackerRef.current
    for (const entry of entries) {
      const sev = detectSeverity(entry.message)
      if (sev) {
        entry.severity = sev
        // TICKET-021: Track error-like entries
        if (ERROR_LIKE_LEVELS.has(sev.level)) {
          const ts = entry.timestamp ? new Date(entry.timestamp).getTime() : Date.now()
          tracker.record(ts)
        }
      }
    }

    if (isPausedRef.current) {
      pausedBufferRef.current.push(...entries)
      if (pausedBufferRef.current.length > MAX_BUFFER) {
        pausedBufferRef.current = pausedBufferRef.current.slice(-MAX_BUFFER)
      }
      setPausedCount(pausedBufferRef.current.length)
    } else {
      setLogs((prev) => {
        const next = [...prev, ...entries]
        return next.length > MAX_LINES
          ? next.slice(next.length - MAX_LINES)
          : next
      })
    }
  }, [])

  // ── WebSocket connection ──
  const connect = useCallback(() => {
    if (!wsUrl) return

    if (wsRef.current) {
      wsRef.current.close()
      wsRef.current = null
    }
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = null
    }

    setWsStatus('connecting')
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      if (!mountedRef.current) return
      setWsStatus('connected')
    }

    ws.onmessage = (event) => {
      if (!mountedRef.current) return
      const text = event.data
      if (!text) return

      const combined = partialLineRef.current + text
      const lines = combined.split('\n')
      partialLineRef.current = lines.pop() || ''

      if (lines.length === 0) return

      const newEntries = lines
        .filter((line) => line.length > 0)
        .map((line) => ({
          id: ++logIdCounter,
          message: line,
          stream: 'stdout',
          timestamp: new Date().toISOString(),
          severity: detectSeverity(line),
        }))

      processLogEntries(newEntries)
    }

    ws.onclose = () => {
      if (!mountedRef.current) return
      setWsStatus('disconnected')
      reconnectTimerRef.current = setTimeout(() => {
        if (mountedRef.current) connect()
      }, 3000)
    }

    ws.onerror = () => {
      if (!mountedRef.current) return
      setWsStatus('error')
    }
  }, [wsUrl, processLogEntries])

  // Connect on mount / containerId change
  useEffect(() => {
    mountedRef.current = true
    connect()

    return () => {
      mountedRef.current = false
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
      }
      if (wsRef.current) {
        wsRef.current.close()
        wsRef.current = null
      }
    }
  }, [connect])

  // ── Reset state when container changes ──
  useEffect(() => {
    setLogs([])
    setPausedCount(0)
    pausedBufferRef.current = []
    partialLineRef.current = ''
    setServerResults(null)
    setShowServerSearch(false)
    setSearchQuery('')
    setUserScrolledUp(false)
    setIsPaused(false)
    setSeverityFilter(null)
    setActiveAlerts([])
    setErrorCount(0)
    alertTrackerRef.current.reset()
  }, [containerId])

  // ── Auto-scroll logic ──
  useEffect(() => {
    if (userScrolledUp || isPaused) return
    const el = logContainerRef.current
    if (el) {
      el.scrollTop = el.scrollHeight
    }
  }, [logs, userScrolledUp, isPaused])

  const handleScroll = useCallback(() => {
    const el = logContainerRef.current
    if (!el) return
    const threshold = 40
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < threshold
    setUserScrolledUp(!atBottom)
  }, [])

  // ── Pause / Resume ──
  const togglePause = useCallback(() => {
    setIsPaused((prev) => {
      const next = !prev
      if (!next) {
        const buffer = pausedBufferRef.current
        pausedBufferRef.current = []
        setPausedCount(0)
        if (buffer.length > 0) {
          setLogs((prevLogs) => {
            const combined = [...prevLogs, ...buffer]
            return combined.length > MAX_LINES
              ? combined.slice(combined.length - MAX_LINES)
              : combined
          })
        }
        setUserScrolledUp(false)
      }
      return next
    })
  }, [])

  // ── Clear logs ──
  const clearLogs = useCallback(() => {
    setLogs([])
    pausedBufferRef.current = []
    setPausedCount(0)
    partialLineRef.current = ''
    setServerResults(null)
    setShowServerSearch(false)
    alertTrackerRef.current.reset()
    setErrorCount(0)
    setActiveAlerts([])
  }, [])

  // ── Server-side search ──
  const searchServer = useCallback(async () => {
    if (!searchQuery.trim()) return

    setSearchLoading(true)
    setServerResults(null)

    try {
      const name = (containerName || '')
        .replace(/^\//, '')
        .trim()

      const params = new URLSearchParams({
        q: searchQuery.trim(),
        limit: '200',
      })
      if (name) params.set('container', name)

      const res = await fetch(`${API_BASE}/logs/search?${params}`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setServerResults(data || [])
      setShowServerSearch(true)
    } catch (err) {
      setServerResults([])
      console.error('Search error:', err)
      setShowServerSearch(true)
    } finally {
      setSearchLoading(false)
    }
  }, [searchQuery, containerName])

  const closeServerSearch = useCallback(() => {
    setShowServerSearch(false)
    setServerResults(null)
  }, [])

  // ── TICKET-021: Alert dismissal ──
  const dismissAlert = useCallback((alertId) => {
    setActiveAlerts((prev) => prev.filter((a) => a.id !== alertId))
  }, [])

  const dismissAllAlerts = useCallback(() => {
    setActiveAlerts([])
  }, [])

  // ── Client-side filtered logs (text search + severity filter) ──
  const displayedLogs = useMemo(() => {
    let filtered = logs

    // TICKET-020: Severity filter
    if (severityFilter === 'error_like') {
      filtered = filtered.filter((l) => l.severity && ERROR_LIKE_LEVELS.has(l.severity.level))
    } else if (severityFilter === 'warn') {
      filtered = filtered.filter((l) => l.severity && l.severity.level === 'warn')
    } else if (severityFilter === 'all_issues') {
      filtered = filtered.filter((l) => l.severity != null)
    }

    // Text search filter
    if (searchQuery.trim()) {
      const q = searchQuery.toLowerCase()
      filtered = filtered.filter((l) => l.message.toLowerCase().includes(q))
    }

    return filtered
  }, [logs, searchQuery, severityFilter])

  // ── TICKET-020: Severity counts ──
  const severityCounts = useMemo(() => {
    const counts = { error: 0, fatal: 0, panic: 0, warn: 0 }
    for (const l of logs) {
      if (l.severity) {
        counts[l.severity.level] = (counts[l.severity.level] || 0) + 1
      }
    }
    return counts
  }, [logs])

  const totalIssues = severityCounts.error + severityCounts.fatal + severityCounts.panic + severityCounts.warn

  // ── Highlight matching text ──
  const highlightMatch = useCallback(
    (text) => {
      if (!searchQuery.trim()) return text
      const q = searchQuery.trim()
      const idx = text.toLowerCase().indexOf(q.toLowerCase())
      if (idx === -1) return text
      return (
        <>
          {text.slice(0, idx)}
          <mark className="bg-yellow-200 text-gray-900 rounded-sm px-0.5">
            {text.slice(idx, idx + q.length)}
          </mark>
          {text.slice(idx + q.length)}
        </>
      )
    },
    [searchQuery],
  )

  // ── Scroll to bottom ──
  const scrollToBottom = useCallback(() => {
    const el = logContainerRef.current
    if (el) {
      el.scrollTop = el.scrollHeight
      setUserScrolledUp(false)
    }
  }, [])

  // ── Connection status badge ──
  const statusBadge = useMemo(() => {
    switch (wsStatus) {
      case 'connected':
        return { label: 'Live', color: 'bg-green-100 text-green-700 border-green-300' }
      case 'connecting':
        return { label: 'Connecting…', color: 'bg-yellow-100 text-yellow-700 border-yellow-300' }
      case 'disconnected':
        return { label: 'Reconnecting…', color: 'bg-yellow-100 text-yellow-700 border-yellow-300' }
      case 'error':
        return { label: 'Error', color: 'bg-red-100 text-red-600 border-red-300' }
      default:
        return { label: 'Idle', color: 'bg-gray-100 text-gray-500 border-gray-200' }
    }
  }, [wsStatus])

  // ── Derived counts ──
  const totalDisplayed = displayedLogs.length
  const filteredOut = logs.length - displayedLogs.length

  // ── Empty state when no container selected ──
  if (!containerId) {
    return (
      <div className="bg-gray-50 border border-gray-200 rounded-lg p-5 min-h-[200px] flex items-center justify-center">
        <p className="text-sm text-gray-400">Select a container to view logs</p>
      </div>
    )
  }

  return (
    <div className="bg-gray-50 border border-gray-200 rounded-lg overflow-hidden flex flex-col min-h-[320px] max-h-[600px] relative">
      {/* ── Toolbar ── */}
      <div className="flex items-center gap-2 px-3 py-2 bg-white border-b border-gray-200 shrink-0 flex-wrap">
        {/* Pause / Resume */}
        <button
          onClick={togglePause}
          className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-xs font-semibold border cursor-pointer transition-colors ${
            isPaused
              ? 'bg-yellow-50 text-yellow-700 border-yellow-300 hover:bg-yellow-100'
              : 'bg-gray-50 text-gray-600 border-gray-300 hover:bg-gray-100'
          }`}
          title={isPaused ? 'Resume live tail' : 'Pause live tail'}
        >
          {isPaused ? (
            <>
              <svg className="w-3 h-3" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>
              Resume
              {pausedCount > 0 && (
                <span className="bg-yellow-200 text-yellow-800 px-1 rounded text-[10px] font-bold">
                  {pausedCount}
                </span>
              )}
            </>
          ) : (
            <>
              <svg className="w-3 h-3" viewBox="0 0 24 24" fill="currentColor"><path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/></svg>
              Pause
            </>
          )}
        </button>

        {/* Clear */}
        <button
          onClick={clearLogs}
          className="inline-flex items-center gap-1 px-2.5 py-1 rounded text-xs font-semibold border border-gray-300 bg-gray-50 text-gray-600 hover:bg-gray-100 cursor-pointer transition-colors"
          title="Clear log display"
        >
          <svg className="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
            <path d="M3 6h18M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2m3 0v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6h14"/>
          </svg>
          Clear
        </button>

        {/* Separator */}
        <span className="w-px h-5 bg-gray-200 mx-0.5" />

        {/* Connection status */}
        <span
          className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-semibold border ${statusBadge.color}`}
        >
          <span className={`w-1.5 h-1.5 rounded-full ${
            wsStatus === 'connected' ? 'bg-green-500 animate-pulse' :
            wsStatus === 'connecting' ? 'bg-yellow-500 animate-pulse' :
            wsStatus === 'error' ? 'bg-red-500' :
            'bg-gray-400'
          }`} />
          {statusBadge.label}
        </span>

        {/* TICKET-021: Error count badge in toolbar */}
        {errorCount > 0 && (
          <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-bold border ${
            errorCount >= 20
              ? 'bg-red-100 text-red-700 border-red-300 animate-pulse'
              : 'bg-red-50 text-red-500 border-red-200'
          }`}>
            <svg className="w-3 h-3" viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/>
            </svg>
            {errorCount} errors/min
          </span>
        )}

        {/* Spacer */}
        <span className="flex-1" />

        {/* TICKET-020: Severity filter buttons */}
        <div className="flex items-center gap-1">
          {/* Total issues badge */}
          {totalIssues > 0 && (
            <button
              onClick={() => setSeverityFilter(severityFilter === 'all_issues' ? null : 'all_issues')}
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[10px] font-semibold border cursor-pointer transition-colors ${
                severityFilter === 'all_issues'
                  ? 'bg-gray-700 text-white border-gray-700'
                  : 'bg-white text-gray-500 border-gray-300 hover:bg-gray-100'
              }`}
              title="Show only lines with issues (ERROR, WARN, PANIC, FATAL)"
            >
              <svg className="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
                <line x1="12" y1="9" x2="12" y2="13"/>
                <line x1="12" y1="17" x2="12.01" y2="17"/>
              </svg>
              Issues
              <span className="bg-red-500 text-white px-1 rounded text-[9px] font-bold">
                {totalIssues}
              </span>
            </button>
          )}

          {/* Error-like only filter */}
          {severityCounts.error + severityCounts.fatal + severityCounts.panic > 0 && (
            <button
              onClick={() => setSeverityFilter(severityFilter === 'error_like' ? null : 'error_like')}
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[10px] font-semibold border cursor-pointer transition-colors ${
                severityFilter === 'error_like'
                  ? 'bg-red-600 text-white border-red-600'
                  : 'bg-red-50 text-red-600 border-red-200 hover:bg-red-100'
              }`}
              title="Show only ERROR / FATAL / PANIC lines"
            >
              ERROR
              <span className="bg-red-500 text-white px-1 rounded text-[9px] font-bold">
                {severityCounts.error + severityCounts.fatal + severityCounts.panic}
              </span>
            </button>
          )}

          {/* WARN filter */}
          {severityCounts.warn > 0 && (
            <button
              onClick={() => setSeverityFilter(severityFilter === 'warn' ? null : 'warn')}
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[10px] font-semibold border cursor-pointer transition-colors ${
                severityFilter === 'warn'
                  ? 'bg-amber-500 text-white border-amber-500'
                  : 'bg-amber-50 text-amber-600 border-amber-200 hover:bg-amber-100'
              }`}
              title="Show only WARN lines"
            >
              WARN
              <span className="bg-amber-500 text-white px-1 rounded text-[9px] font-bold">
                {severityCounts.warn}
              </span>
            </button>
          )}

          {/* Clear filter */}
          {severityFilter && (
            <button
              onClick={() => setSeverityFilter(null)}
              className="text-gray-400 hover:text-gray-600 cursor-pointer ml-0.5"
              title="Clear severity filter"
            >
              <svg className="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M18 6L6 18M6 6l12 12"/>
              </svg>
            </button>
          )}
        </div>

        {/* Separator */}
        <span className="w-px h-5 bg-gray-200 mx-0.5" />

        {/* Search */}
        <div className="flex items-center gap-1.5">
          <div className="relative">
            <svg className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-gray-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
              <circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/>
            </svg>
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Filter logs…"
              className="w-44 pl-7 pr-2 py-1 text-xs border border-gray-300 rounded bg-gray-50 focus:outline-none focus:border-blue-400 focus:bg-white focus:ring-1 focus:ring-blue-200 transition-colors"
            />
            {searchQuery && (
              <button
                onClick={() => { setSearchQuery(''); setServerResults(null); setShowServerSearch(false) }}
                className="absolute right-1.5 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600 cursor-pointer"
              >
                <svg className="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                  <path d="M18 6L6 18M6 6l12 12"/>
                </svg>
              </button>
            )}
          </div>

          {/* Search in history button */}
          <button
            onClick={searchServer}
            disabled={!searchQuery.trim() || searchLoading}
            className="px-2 py-1 text-[10px] font-semibold rounded border border-gray-300 bg-white text-gray-500 hover:bg-gray-50 hover:text-gray-700 disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer transition-colors whitespace-nowrap"
            title="Search stored logs in database"
          >
            {searchLoading ? (
              <span className="inline-flex items-center gap-1">
                <span className="w-2.5 h-2.5 border border-gray-400 border-t-transparent rounded-full animate-spin" />
                Searching…
              </span>
            ) : (
              'History'
            )}
          </button>
        </div>

        {/* Line count */}
        <span className="text-[10px] text-gray-400 font-mono whitespace-nowrap">
          {totalDisplayed.toLocaleString()} lines
          {filteredOut > 0 && (
            <span className="text-yellow-600"> ({filteredOut.toLocaleString()} hidden)</span>
          )}
        </span>
      </div>

      {/* ── TICKET-021: Alert banner ── */}
      {activeAlerts.length > 0 && (
        <div className="border-b border-gray-200 shrink-0">
          <AlertBanner
            alerts={activeAlerts}
            onDismiss={dismissAlert}
            onDismissAll={dismissAllAlerts}
          />
        </div>
      )}

      {/* ── Server search results ── */}
      {showServerSearch && serverResults && (
        <div className="border-b border-blue-200 bg-blue-50/60 px-3 py-2 shrink-0">
          <div className="flex items-center justify-between mb-1.5">
            <span className="text-xs font-semibold text-blue-800">
              History: {serverResults.length} result{serverResults.length !== 1 ? 's' : ''} for &ldquo;{searchQuery.trim()}&rdquo;
            </span>
            <button
              onClick={closeServerSearch}
              className="text-blue-500 hover:text-blue-700 cursor-pointer"
            >
              <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M18 6L6 18M6 6l12 12"/>
              </svg>
            </button>
          </div>
          {serverResults.length === 0 ? (
            <p className="text-xs text-blue-600/70">No matching logs found in the database.</p>
          ) : (
            <div className="max-h-32 overflow-y-auto bg-white border border-blue-100 rounded p-2 font-mono text-xs leading-relaxed">
              {serverResults.map((entry) => (
                <div
                  key={entry.id}
                  className={`flex gap-2 py-0.5 ${
                    entry.stream === 'stderr' ? 'text-red-700' : 'text-gray-700'
                  }`}
                >
                  <span className="text-[10px] text-gray-400 shrink-0 select-none">
                    {formatTime(entry.timestamp)}
                  </span>
                  <span className="text-[10px] text-gray-400 shrink-0 w-8 text-right select-none">
                    {entry.stream}
                  </span>
                  <span className="break-all whitespace-pre-wrap">
                    {highlightMatch(entry.message)}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* ── Log display area ── */}
      <div
        ref={logContainerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto bg-gray-900 font-mono text-xs leading-relaxed relative"
      >
        {displayedLogs.length === 0 ? (
          <div className="flex items-center justify-center h-full min-h-[160px]">
            <div className="text-center">
              {wsStatus === 'connecting' ? (
                <>
                  <div className="w-5 h-5 border-2 border-gray-600 border-t-gray-300 rounded-full animate-spin mx-auto mb-2" />
                  <p className="text-gray-500">Connecting to log stream…</p>
                </>
              ) : wsStatus === 'error' ? (
                <>
                  <p className="text-red-400 mb-2">Connection error</p>
                  <button
                    onClick={connect}
                    className="px-3 py-1 text-xs border border-gray-600 text-gray-300 rounded hover:bg-gray-800 cursor-pointer transition-colors"
                  >
                    Retry
                  </button>
                </>
              ) : (
                <p className="text-gray-500">
                  {severityFilter ? 'No matching log entries for the current filter.' : 'Waiting for logs…'}
                </p>
              )}
            </div>
          </div>
        ) : (
          <div className="min-h-full">
            {displayedLogs.map((log, i) => {
              // TICKET-020: Determine border color based on severity
              let borderColor = 'border-transparent'
              let bgTint = ''
              if (log.severity) {
                switch (log.severity.level) {
                  case 'fatal':
                    borderColor = 'border-fuchsia-500/70'
                    bgTint = 'bg-fuchsia-500/10'
                    break
                  case 'panic':
                    borderColor = 'border-purple-500/60'
                    bgTint = 'bg-purple-500/10'
                    break
                  case 'error':
                    borderColor = 'border-red-500/60'
                    bgTint = 'bg-red-500/10'
                    break
                  case 'warn':
                    borderColor = 'border-amber-500/50'
                    bgTint = 'bg-amber-500/5'
                    break
                }
              }
              // Also keep stderr styling
              if (log.stream === 'stderr' && !log.severity) {
                borderColor = 'border-red-500/60'
                bgTint = 'bg-red-500/5'
              }

              return (
                <div
                  key={log.id}
                  className={`flex hover:bg-white/5 border-l-2 ${borderColor} ${bgTint}`}
                >
                  {/* Line number */}
                  <span className="text-gray-600 text-[10px] w-10 text-right pr-2 select-none shrink-0 pt-px">
                    {i + 1}
                  </span>

                  {/* Timestamp */}
                  <span className="text-gray-400 text-[10px] w-16 shrink-0 select-none pt-px">
                    {formatTime(log.timestamp)}
                  </span>

                  {/* TICKET-020: Severity badge */}
                  <span className="w-14 shrink-0 pt-px flex items-start">
                    {log.severity && (
                      <span className={`inline-block px-1.5 py-0 text-[9px] font-bold rounded ${log.severity.color}`}>
                        {log.severity.label}
                      </span>
                    )}
                  </span>

                  {/* Message */}
                  <span
                    className={`whitespace-pre-wrap break-all ${
                      log.stream === 'stderr' && !log.severity
                        ? 'text-red-300'
                        : log.severity?.level === 'fatal' || log.severity?.level === 'panic'
                          ? 'text-fuchsia-200'
                          : log.severity?.level === 'error'
                            ? 'text-red-300'
                            : log.severity?.level === 'warn'
                              ? 'text-amber-200'
                              : 'text-gray-200'
                    }`}
                  >
                    {searchQuery.trim() ? highlightMatch(log.message) : log.message}
                  </span>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {/* ── Scroll-to-bottom FAB ── */}
      {userScrolledUp && (
        <button
          onClick={scrollToBottom}
          className="absolute bottom-3 right-3 px-2.5 py-1 text-[10px] font-semibold bg-gray-700 text-gray-200 border border-gray-600 rounded shadow-lg hover:bg-gray-600 cursor-pointer transition-colors"
          style={{ marginBottom: '4px' }}
        >
          ↓ Bottom
        </button>
      )}
    </div>
  )
}
