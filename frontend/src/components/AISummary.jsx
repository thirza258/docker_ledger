import { useState, useCallback } from 'react'

const API_BASE = '/api'

export default function AISummary({ containerName }) {
  const [summary, setSummary] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const [expanded, setExpanded] = useState(false)
  const [hoursBack, setHoursBack] = useState(24)
  const [limit, setLimit] = useState(2000)
  const [showConfig, setShowConfig] = useState(false)

  const fetchSummary = useCallback(async () => {
    setLoading(true)
    setError(null)
    setSummary(null)

    try {
      const params = new URLSearchParams({
        hours_back: String(hoursBack),
        limit: String(limit),
      })

      let url
      if (containerName) {
        const name = containerName.replace(/^\//, '').trim()
        params.set('container_name', name)
        url = `${API_BASE}/logs/summarize/container?${params}`
      } else {
        url = `${API_BASE}/logs/summarize?${params}`
      }

      const res = await fetch(url)
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const data = await res.json()
      setSummary(data)
      setExpanded(true)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [containerName, hoursBack, limit])

  const dismiss = useCallback(() => {
    setSummary(null)
    setError(null)
    setExpanded(false)
  }, [])

  return (
    <div className="relative">
      {/* ── Trigger button ── */}
      <div className="flex items-center gap-1.5">
        <button
          onClick={fetchSummary}
          disabled={loading}
          className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-xs font-semibold border border-purple-300 bg-purple-50 text-purple-700 hover:bg-purple-100 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer transition-colors"
          title={containerName ? `AI summary for ${containerName}` : 'AI summary of all container logs'}
        >
          {loading ? (
            <>
              <span className="w-3 h-3 border-2 border-purple-400 border-t-transparent rounded-full animate-spin" />
              Analyzing…
            </>
          ) : (
            <>
              <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09zM18.259 8.715L18 9.75l-.259-1.035a3.375 3.375 0 00-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 002.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 002.455 2.456L21.75 6l-1.036.259a3.375 3.375 0 00-2.455 2.456zM20.328 17.676L20.25 18l-.078-.676a1.125 1.125 0 00-.787-.787L18.25 16.5l.457-.037a1.125 1.125 0 00.787-.787L19.5 15l.078.676a1.125 1.125 0 00.787.787L21.75 16.5l-.457.037a1.125 1.125 0 00-.787.787z"/>
              </svg>
              AI Summary
            </>
          )}
        </button>

        {/* Config toggle */}
        <button
          onClick={() => setShowConfig(!showConfig)}
          className={`p-1 rounded text-xs border cursor-pointer transition-colors ${
            showConfig
              ? 'bg-gray-200 text-gray-700 border-gray-400'
              : 'bg-white text-gray-400 border-gray-200 hover:border-gray-300 hover:text-gray-500'
          }`}
          title="Configure summary parameters"
        >
          <svg className="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <circle cx="12" cy="12" r="3"/>
            <path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/>
          </svg>
        </button>
      </div>

      {/* ── Config panel ── */}
      {showConfig && (
        <div className="absolute top-full left-0 mt-1 z-20 bg-white border border-gray-200 rounded-lg shadow-lg p-3 w-56">
          <div className="flex flex-col gap-2">
            <label className="flex flex-col gap-0.5">
              <span className="text-[10px] font-semibold text-gray-400 uppercase tracking-wide">Hours back</span>
              <input
                type="number"
                value={hoursBack}
                onChange={(e) => setHoursBack(Math.max(1, Math.min(168, parseInt(e.target.value) || 24)))}
                min="1"
                max="168"
                className="w-full px-2 py-1 text-xs border border-gray-300 rounded bg-gray-50 focus:outline-none focus:border-purple-400 focus:ring-1 focus:ring-purple-200"
              />
            </label>
            <label className="flex flex-col gap-0.5">
              <span className="text-[10px] font-semibold text-gray-400 uppercase tracking-wide">Log limit</span>
              <input
                type="number"
                value={limit}
                onChange={(e) => setLimit(Math.max(100, Math.min(5000, parseInt(e.target.value) || 2000)))}
                min="100"
                max="5000"
                step="100"
                className="w-full px-2 py-1 text-xs border border-gray-300 rounded bg-gray-50 focus:outline-none focus:border-purple-400 focus:ring-1 focus:ring-purple-200"
              />
            </label>
          </div>
        </div>
      )}

      {/* ── Error display ── */}
      {error && (
        <div className="mt-2 flex items-start gap-2 px-3 py-2 rounded border border-red-200 bg-red-50 text-sm">
          <svg className="w-4 h-4 text-red-500 shrink-0 mt-0.5" viewBox="0 0 24 24" fill="currentColor">
            <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/>
          </svg>
          <span className="flex-1 text-red-700 text-xs">{error}</span>
          <button onClick={() => setError(null)} className="text-red-400 hover:text-red-600 shrink-0 cursor-pointer">
            <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
              <path d="M18 6L6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>
      )}

      {/* ── Summary result panel ── */}
      {summary && expanded && (
        <div className="mt-2 bg-white border border-purple-200 rounded-lg shadow-sm overflow-hidden">
          {/* Header */}
          <div className="flex items-center justify-between px-3 py-2 bg-purple-50 border-b border-purple-200">
            <div className="flex items-center gap-1.5">
              <svg className="w-3.5 h-3.5 text-purple-500" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09zM18.259 8.715L18 9.75l-.259-1.035a3.375 3.375 0 00-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 002.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 002.455 2.456L21.75 6l-1.036.259a3.375 3.375 0 00-2.455 2.456zM20.328 17.676L20.25 18l-.078-.676a1.125 1.125 0 00-.787-.787L18.25 16.5l.457-.037a1.125 1.125 0 00.787-.787L19.5 15l.078.676a1.125 1.125 0 00.787.787L21.75 16.5l-.457.037a1.125 1.125 0 00-.787.787z"/>
              </svg>
              <span className="text-xs font-semibold text-purple-800">
                {containerName ? `AI Summary: ${containerName}` : 'AI Summary: All Containers'}
              </span>
              <span className="text-[10px] text-purple-400">last {hoursBack}h</span>
            </div>
            <button onClick={dismiss} className="text-purple-400 hover:text-purple-600 cursor-pointer">
              <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M18 6L6 18M6 6l12 12"/>
              </svg>
            </button>
          </div>

          {/* Content */}
          <div className="p-3 space-y-3 max-h-80 overflow-y-auto">
            {/* Top errors */}
            {summary.top_errors && summary.top_errors.length > 0 && summary.top_errors[0] !== 'Could not parse AI output' && (
              <Section title="Top Errors" color="red">
                <ul className="list-disc list-inside space-y-0.5">
                  {summary.top_errors.map((err, i) => (
                    <li key={i} className="text-xs text-gray-700">{err}</li>
                  ))}
                </ul>
              </Section>
            )}

            {/* Most failing containers */}
            {summary.most_failing_containers && summary.most_failing_containers.length > 0 && (
              <Section title="Most Failing Containers" color="amber">
                <div className="flex flex-wrap gap-1.5">
                  {summary.most_failing_containers.map((c, i) => (
                    <span key={i} className="inline-block px-2 py-0.5 rounded text-[10px] font-semibold bg-amber-100 text-amber-800">
                      {c}
                    </span>
                  ))}
                </div>
              </Section>
            )}

            {/* Suggested causes */}
            {summary.suggested_causes && summary.suggested_causes.length > 0 && (
              <Section title="Suggested Causes" color="blue">
                <ul className="list-disc list-inside space-y-0.5">
                  {summary.suggested_causes.map((cause, i) => (
                    <li key={i} className="text-xs text-gray-700">{cause}</li>
                  ))}
                </ul>
              </Section>
            )}

            {/* Patterns (per-container summary) */}
            {summary.patterns && summary.patterns.length > 0 && (
              <Section title="Patterns" color="purple">
                <ul className="list-disc list-inside space-y-0.5">
                  {summary.patterns.map((p, i) => (
                    <li key={i} className="text-xs text-gray-700">{p}</li>
                  ))}
                </ul>
              </Section>
            )}

            {/* Raw response fallback */}
            {summary.raw_response && summary.top_errors && summary.top_errors[0] === 'Could not parse AI output' && (
              <Section title="Raw AI Response" color="gray">
                <pre className="text-xs text-gray-600 whitespace-pre-wrap font-mono bg-gray-50 p-2 rounded border border-gray-200 max-h-40 overflow-y-auto">
                  {summary.raw_response}
                </pre>
              </Section>
            )}
          </div>

          {/* Footer */}
          <div className="px-3 py-1.5 bg-gray-50 border-t border-gray-100 flex items-center gap-2">
            <button
              onClick={fetchSummary}
              disabled={loading}
              className="text-[10px] text-purple-600 hover:text-purple-800 font-semibold disabled:opacity-50 cursor-pointer transition-colors"
            >
              Refresh
            </button>
            <span className="text-[10px] text-gray-400">·</span>
            <button
              onClick={dismiss}
              className="text-[10px] text-gray-400 hover:text-gray-600 cursor-pointer transition-colors"
            >
              Close
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

/** Small reusable section for summary content blocks. */
function Section({ title, color, children }) {
  const borderColors = {
    red: 'border-red-200',
    amber: 'border-amber-200',
    blue: 'border-blue-200',
    purple: 'border-purple-200',
    gray: 'border-gray-200',
  }
  const bgColors = {
    red: 'bg-red-50/50',
    amber: 'bg-amber-50/50',
    blue: 'bg-blue-50/50',
    purple: 'bg-purple-50/50',
    gray: 'bg-gray-50/50',
  }
  const textColors = {
    red: 'text-red-700',
    amber: 'text-amber-700',
    blue: 'text-blue-700',
    purple: 'text-purple-700',
    gray: 'text-gray-600',
  }

  return (
    <div className={`border ${borderColors[color]} ${bgColors[color]} rounded p-2.5`}>
      <h4 className={`text-[11px] font-semibold ${textColors[color]} uppercase tracking-wide mb-1.5`}>
        {title}
      </h4>
      {children}
    </div>
  )
}
