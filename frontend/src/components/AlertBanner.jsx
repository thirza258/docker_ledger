const SEVERITY_ICONS = {
  error: (
    <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
      <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/>
    </svg>
  ),
  warn: (
    <svg className="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
      <path d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/>
    </svg>
  ),
}

const SEVERITY_STYLES = {
  error: {
    banner: 'bg-red-600 border-red-700 text-white',
    badge: 'bg-red-200 text-red-800',
  },
  critical: {
    banner: 'bg-red-800 border-red-900 text-white animate-pulse',
    badge: 'bg-red-300 text-red-900',
  },
  warn: {
    banner: 'bg-amber-500 border-amber-600 text-white',
    badge: 'bg-amber-200 text-amber-800',
  },
}

export default function AlertBanner({ alerts, onDismiss, onDismissAll }) {
  if (!alerts || alerts.length === 0) return null

  return (
    <div className="flex flex-col gap-1.5">
      {alerts.map((alert) => {
        const style = SEVERITY_STYLES[alert.level] || SEVERITY_STYLES.warn

        return (
          <div
            key={alert.id}
            className={`flex items-center gap-2 px-3 py-2 rounded border text-sm ${style.banner} transition-all`}
          >
            <span className="shrink-0">
              {SEVERITY_ICONS[alert.level === 'critical' ? 'error' : 'error']}
            </span>

            <span className="flex-1 font-medium text-sm">
              {alert.message}
            </span>

            {alert.count !== undefined && (
              <span className={`px-1.5 py-0.5 rounded text-xs font-bold ${style.badge}`}>
                {alert.count}
              </span>
            )}

            <span className="text-xs opacity-70 font-mono whitespace-nowrap">
              {alert.time}
            </span>

            <button
              onClick={() => onDismiss(alert.id)}
              className="shrink-0 ml-1 p-0.5 rounded hover:bg-white/20 cursor-pointer transition-colors"
              title="Dismiss"
            >
              <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M18 6L6 18M6 6l12 12"/>
              </svg>
            </button>
          </div>
        )
      })}

      {alerts.length > 1 && (
        <button
          onClick={onDismissAll}
          className="self-end text-xs text-gray-400 hover:text-gray-600 cursor-pointer transition-colors"
        >
          Dismiss all
        </button>
      )}
    </div>
  )
}
