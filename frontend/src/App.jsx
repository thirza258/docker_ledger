import { useState, useEffect, useCallback } from 'react'
import LogViewer from './components/LogViewer.jsx'
import AISummary from './components/AISummary.jsx'

const API_BASE = '/api'

function App() {
  const [containers, setContainers] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [selected, setSelected] = useState(null)
  const [stats, setStats] = useState(null)

  const fetchContainers = useCallback(async () => {
    try {
      const res = await fetch(`${API_BASE}/containers`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setContainers(data.containers || [])
      setError(null)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    let mounted = true
   
    const initTimer = setTimeout(() => {
      if (mounted) fetchContainers()
    }, 0)

    const interval = setInterval(() => {
      if (mounted) fetchContainers()
    }, 10000)

    return () => {
      mounted = false
      clearTimeout(initTimer)
      clearInterval(interval)
    }
  }, [fetchContainers])

  const fetchStats = async (id) => {
    try {
      const res = await fetch(`${API_BASE}/containers/${id}/stats`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setStats(data)
    } catch (err) {
      setStats({ error: err.message })
    }
  }

  const selectContainer = (c) => {
    setSelected(c)
    setStats(null)
    fetchStats(c.Id)
  }

  const running = containers.filter((c) => c.State === 'running').length
  const stopped = containers.length - running

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center min-h-screen gap-4 text-gray-400">
        <div className="w-8 h-8 border-2 border-gray-200 border-t-blue-500 rounded-full animate-spin" />
        <p>Loading containers…</p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center min-h-screen gap-3 text-center px-6">
        <div className="w-12 h-12 rounded-full bg-red-50 text-red-500 flex items-center justify-center text-2xl font-bold mb-2">
          !
        </div>
        <h2 className="text-lg font-semibold text-gray-900">Connection Error</h2>
        <p className="text-gray-500">Could not connect to the DockerLedger API.</p>
        <p className="text-xs text-gray-400 font-mono bg-gray-50 px-3 py-1.5 rounded">{error}</p>
        <button
          onClick={fetchContainers}
          className="mt-3 px-5 py-2 border border-gray-200 rounded-md text-sm text-gray-700 bg-white hover:border-gray-400 transition-colors cursor-pointer"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="min-h-screen flex flex-col bg-white text-gray-700">
      {/* Header */}
      <header className="sticky top-0 z-10 bg-white border-b border-gray-200">
        <div className="max-w-6xl mx-auto px-6 py-4 flex items-baseline gap-3 flex-wrap">
          <h1 className="text-xl font-bold text-gray-900 tracking-tight">DockerLedger</h1>
          <span className="text-sm text-gray-400 font-normal">Container Monitor</span>
          <span className="flex-1" />
          <AISummary />
        </div>
      </header>

      <main className="max-w-6xl w-full mx-auto px-6 py-6 flex-1">
        {/* Stats cards */}
        <div className="flex gap-4 mb-6 max-sm:flex-col max-sm:gap-2.5">
          <div className="flex-1 bg-gray-50 border border-gray-200 rounded-lg p-5 flex flex-col items-center gap-1">
            <span className="text-3xl font-bold text-gray-900">{containers.length}</span>
            <span className="text-xs text-gray-400 uppercase tracking-wide">Total</span>
          </div>
          <div className="flex-1 bg-gray-50 border border-gray-200 rounded-lg p-5 flex flex-col items-center gap-1">
            <span className="text-3xl font-bold text-green-500">{running}</span>
            <span className="text-xs text-gray-400 uppercase tracking-wide">Running</span>
          </div>
          <div className="flex-1 bg-gray-50 border border-gray-200 rounded-lg p-5 flex flex-col items-center gap-1">
            <span className="text-3xl font-bold text-red-500">{stopped}</span>
            <span className="text-xs text-gray-400 uppercase tracking-wide">Stopped</span>
          </div>
        </div>

        {/* Content split */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6 items-start">
          {/* Container list */}
          <div>
            <h2 className="text-base font-semibold text-gray-900 mb-3">Containers</h2>
            {containers.length === 0 ? (
              <p className="text-sm text-gray-400 py-6 text-center">No containers found.</p>
            ) : (
              <div className="border border-gray-200 rounded-lg overflow-hidden">
                <table className="w-full border-collapse text-sm">
                  <thead className="bg-gray-50">
                    <tr>
                      <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                        Status
                      </th>
                      <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                        Name
                      </th>
                      <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                        Image
                      </th>
                      <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                        ID
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {containers.map((c) => {
                      const id = c.Id || ''
                      const isSelected = selected && selected.Id === id
                      return (
                        <tr
                          key={id}
                          onClick={() => selectContainer(c)}
                          className={`cursor-pointer transition-colors border-b border-gray-100 last:border-b-0 hover:bg-gray-50 ${
                            isSelected ? 'bg-blue-50' : ''
                          }`}
                        >
                          <td className="px-3.5 py-2.5">
                            <span
                              className={`inline-block w-2 h-2 rounded-full ${
                                c.State === 'running' ? 'bg-green-500' : 'bg-red-500'
                              }`}
                              title={c.State}
                            />
                          </td>
                          <td className="px-3.5 py-2.5 font-medium text-gray-900 max-w-[200px] overflow-hidden text-ellipsis whitespace-nowrap">
                            {(c.Names || []).join(', ')}
                          </td>
                          <td className="px-3.5 py-2.5 text-gray-400 max-w-[220px] overflow-hidden text-ellipsis whitespace-nowrap">
                            {c.Image}
                          </td>
                          <td className="px-3.5 py-2.5 font-mono text-[13px] text-gray-500">
                            {id.substring(0, 12)}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          {/* Detail panel */}
          <div className="bg-gray-50 border border-gray-200 rounded-lg p-5 min-h-[200px]">
            {selected ? (
              <>
                <h2 className="text-base font-semibold text-gray-900 mb-3">
                  {(selected.Names || []).join(', ')}
                </h2>

                <div className="grid grid-cols-2 gap-3 mb-4 max-[480px]:grid-cols-1">
                  <div className="flex flex-col gap-0.5">
                    <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">ID</span>
                    <span className="text-sm text-gray-900 font-mono break-all">{selected.Id}</span>
                  </div>
                  <div className="flex flex-col gap-0.5">
                    <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">Image</span>
                    <span className="text-sm text-gray-900">{selected.Image}</span>
                  </div>
                  <div className="flex flex-col gap-0.5">
                    <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">State</span>
                    <span
                      className={`inline-block px-2 py-0.5 rounded text-xs font-semibold w-fit ${
                        selected.State === 'running'
                          ? 'bg-green-50 text-green-600'
                          : 'bg-red-50 text-red-600'
                      }`}
                    >
                      {selected.State}
                    </span>
                  </div>
                  <div className="flex flex-col gap-0.5">
                    <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">Status</span>
                    <span className="text-sm text-gray-900">{selected.Status}</span>
                  </div>
                </div>

                {stats && (
                  <div className="mt-4 pt-4 border-t border-gray-200">
                    <h3 className="text-sm font-semibold text-gray-900 mb-2.5">Resource Usage</h3>
                    {stats.error ? (
                      <p className="text-[13px] text-gray-400 font-mono">{stats.error}</p>
                    ) : (
                      <div className="grid grid-cols-2 gap-3 max-[480px]:grid-cols-1">
                        {stats.cpu_stats?.online_cpus !== undefined && (
                          <div className="flex flex-col gap-0.5">
                            <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">CPUs</span>
                            <span className="text-sm text-gray-900">{stats.cpu_stats.online_cpus}</span>
                          </div>
                        )}
                        {stats.memory_stats?.usage !== undefined && (
                          <div className="flex flex-col gap-0.5">
                            <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">Memory</span>
                            <span className="text-sm text-gray-900">{formatBytes(stats.memory_stats.usage)}</span>
                          </div>
                        )}
                        {stats.memory_stats?.limit !== undefined && (
                          <div className="flex flex-col gap-0.5">
                            <span className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">Memory Limit</span>
                            <span className="text-sm text-gray-900">{formatBytes(stats.memory_stats.limit)}</span>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )}

                <div className="mt-4 pt-4 border-t border-gray-200">
                  <h3 className="text-sm font-semibold text-gray-900 mb-2.5">AI Analysis</h3>
                  <AISummary containerName={(selected.Names || [])[0] || ''} />
                </div>

              </>
            ) : (
              <div className="flex items-center justify-center min-h-[200px] text-sm text-gray-400">
                <p>Select a container to view details</p>
              </div>
            )}
          </div>
        </div>

        {/* Full-width log viewer */}
        {selected && (
          <div className="mt-6">
            <h2 className="text-base font-semibold text-gray-900 mb-3">
              Logs &mdash; {(selected.Names || []).join(', ')}
            </h2>
            <LogViewer
              containerId={selected.Id}
              containerName={(selected.Names || [])[0] || ''}
            />
          </div>
        )}
      </main>
    </div>
  )
}

function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

export default App
