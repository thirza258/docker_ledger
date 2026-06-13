import { useState, useEffect, useCallback } from 'react'

const API_BASE = '/api'

export default function WakeproxyPage() {
  const [containers, setContainers] = useState([])
  const [services, setServices] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [formHost, setFormHost] = useState('')
  const [formContainer, setFormContainer] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [formError, setFormError] = useState(null)
  const [formSuccess, setFormSuccess] = useState(null)
  const [actionLoading, setActionLoading] = useState(null) // name of service being toggled

  const fetchData = useCallback(async () => {
    try {
      const [containersRes, servicesRes] = await Promise.all([
        fetch(`${API_BASE}/containers`),
        fetch(`${API_BASE}/wakeproxy/services`),
      ])

      if (!containersRes.ok) throw new Error(`Containers HTTP ${containersRes.status}`)

      const containersData = await containersRes.json()
      setContainers(containersData.containers || [])

      if (servicesRes.ok) {
        const servicesData = await servicesRes.json()
        setServices(Array.isArray(servicesData) ? servicesData : [])
      }
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
      if (mounted) fetchData()
    }, 0)

    return () => {
      mounted = false
      clearTimeout(initTimer)
    }
  }, [fetchData])

  const handleSubmit = async (e) => {
    e.preventDefault()
    setFormError(null)
    setFormSuccess(null)

    if (!formHost.trim()) {
      setFormError('Host is required')
      return
    }
    if (!formContainer) {
      setFormError('Please select a container')
      return
    }

    setSubmitting(true)
    try {
      const res = await fetch(`${API_BASE}/wakeproxy/services`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          host: formHost.trim(),
          container: formContainer,
          port: 80,
        }),
      })

      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }

      setFormHost('')
      setFormContainer('')
      setFormSuccess(`Service "${formHost.trim()}" created`)
      fetchData()
    } catch (err) {
      setFormError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const toggleActive = async (svc) => {
    setActionLoading(svc.name)
    try {
      const action = svc.active ? 'deactivate' : 'activate'
      const res = await fetch(`${API_BASE}/wakeproxy/services/${encodeURIComponent(svc.name)}/${action}`, {
        method: 'POST',
      })

      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }

      fetchData()
    } catch (err) {
      setFormError(err.message)
    } finally {
      setActionLoading(null)
    }
  }

  // Get container display name from Names array
  const containerDisplayName = (c) => {
    const names = c.Names || []
    return names.length > 0 ? names.join(', ') : c.Id?.substring(0, 12) || ''
  }

  // Only running containers in the dropdown
  const runningContainers = containers.filter((c) => c.State === 'running')

  const stateLabels = {
    0: 'Stopped',
    1: 'Starting',
    2: 'Running',
  }

  const stateColors = {
    0: 'bg-red-100 text-red-600',
    1: 'bg-yellow-100 text-yellow-600',
    2: 'bg-green-100 text-green-600',
  }

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-4 text-gray-400">
        <div className="w-8 h-8 border-2 border-gray-200 border-t-blue-500 rounded-full animate-spin" />
        <p>Loading wakeproxy services…</p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-3 text-center px-6">
        <div className="w-12 h-12 rounded-full bg-red-50 text-red-500 flex items-center justify-center text-2xl font-bold mb-2">
          !
        </div>
        <h2 className="text-lg font-semibold text-gray-900">Connection Error</h2>
        <p className="text-gray-500">Could not load wakeproxy data.</p>
        <p className="text-xs text-gray-400 font-mono bg-gray-50 px-3 py-1.5 rounded">{error}</p>
        <button
          onClick={fetchData}
          className="mt-3 px-5 py-2 border border-gray-200 rounded-md text-sm text-gray-700 bg-white hover:border-gray-400 transition-colors cursor-pointer"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {/* ── Add Service Form ── */}
      <div className="bg-gray-50 border border-gray-200 rounded-lg p-5">
        <h2 className="text-base font-semibold text-gray-900 mb-4">Add WakeProxy Service</h2>

        <form onSubmit={handleSubmit} className="space-y-4">
          {/* Container dropdown */}
          <div className="flex flex-col gap-1.5">
            <label htmlFor="wp-container" className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">
              Container
            </label>
            <select
              id="wp-container"
              value={formContainer}
              onChange={(e) => setFormContainer(e.target.value)}
              className="w-full max-w-md px-3 py-2 text-sm border border-gray-300 rounded bg-white focus:outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-200 transition-colors"
            >
              <option value="">-- Select a running container --</option>
              {runningContainers.map((c) => (
                <option key={c.Id} value={(c.Names || [])[0]?.replace(/^\//, '') || c.Id}>
                  {containerDisplayName(c)} — {c.Image}
                </option>
              ))}
            </select>
            {runningContainers.length === 0 && (
              <p className="text-xs text-gray-400">No running containers found.</p>
            )}
          </div>

          {/* Host input */}
          <div className="flex flex-col gap-1.5">
            <label htmlFor="wp-host" className="text-[11px] text-gray-400 uppercase tracking-wide font-semibold">
              Host
            </label>
            <input
              id="wp-host"
              type="text"
              value={formHost}
              onChange={(e) => setFormHost(e.target.value)}
              placeholder="e.g. myapp.local"
              className="w-full max-w-md px-3 py-2 text-sm border border-gray-300 rounded bg-white focus:outline-none focus:border-blue-400 focus:ring-1 focus:ring-blue-200 transition-colors"
            />
            <p className="text-xs text-gray-400">The hostname that will be proxied to this container.</p>
          </div>

          {/* Submit */}
          <div className="flex items-center gap-3">
            <button
              type="submit"
              disabled={submitting || runningContainers.length === 0}
              className="inline-flex items-center gap-2 px-4 py-2 rounded text-sm font-semibold bg-gray-900 text-white border border-gray-900 hover:bg-gray-800 disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer transition-colors"
            >
              {submitting ? (
                <>
                  <span className="w-3.5 h-3.5 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                  Adding…
                </>
              ) : (
                'Add Service'
              )}
            </button>

            {formError && (
              <p className="text-xs text-red-600">{formError}</p>
            )}
            {formSuccess && (
              <p className="text-xs text-green-600">{formSuccess}</p>
            )}
          </div>
        </form>
      </div>

      {/* ── Configured Services List ── */}
      <div>
        <h2 className="text-base font-semibold text-gray-900 mb-3">Configured Services</h2>

        {services.length === 0 ? (
          <div className="bg-gray-50 border border-gray-200 rounded-lg p-8 text-center">
            <p className="text-sm text-gray-400">No wakeproxy services configured yet.</p>
            <p className="text-xs text-gray-400 mt-1">Add one using the form above.</p>
          </div>
        ) : (
          <div className="border border-gray-200 rounded-lg overflow-hidden">
            <table className="w-full border-collapse text-sm">
              <thead className="bg-gray-50">
                <tr>
                  <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                    Host
                  </th>
                  <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                    Container
                  </th>
                  <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                    Port
                  </th>
                  <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                    State
                  </th>
                  <th className="text-left px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                    Active
                  </th>
                  <th className="text-right px-3.5 py-2.5 font-semibold text-xs text-gray-400 uppercase tracking-wide border-b border-gray-200">
                    Action
                  </th>
                </tr>
              </thead>
              <tbody>
                {services.map((svc) => {
                  const isToggling = actionLoading === svc.name
                  return (
                    <tr key={svc.name} className="border-b border-gray-100 last:border-b-0 hover:bg-gray-50 transition-colors">
                      <td className="px-3.5 py-2.5 font-medium text-gray-900 font-mono text-[13px]">
                        {svc.host}
                      </td>
                      <td className="px-3.5 py-2.5 text-gray-700">
                        {svc.container}
                      </td>
                      <td className="px-3.5 py-2.5 text-gray-400 font-mono text-[13px]">
                        {svc.port || 80}
                      </td>
                      <td className="px-3.5 py-2.5">
                        <span className={`inline-block px-2 py-0.5 rounded text-[11px] font-semibold ${stateColors[svc.state] || 'bg-gray-100 text-gray-500'}`}>
                          {stateLabels[svc.state] || svc.state}
                        </span>
                      </td>
                      <td className="px-3.5 py-2.5">
                        <span className={`inline-block w-2 h-2 rounded-full ${svc.active ? 'bg-green-500' : 'bg-gray-300'}`} />
                        <span className="ml-1.5 text-xs text-gray-500">
                          {svc.active ? 'Active' : 'Inactive'}
                        </span>
                      </td>
                      <td className="px-3.5 py-2.5 text-right">
                        <button
                          onClick={() => toggleActive(svc)}
                          disabled={isToggling}
                          className={`inline-flex items-center gap-1 px-2.5 py-1 rounded text-[11px] font-semibold border cursor-pointer transition-colors disabled:opacity-40 disabled:cursor-not-allowed ${
                            svc.active
                              ? 'bg-red-50 text-red-600 border-red-200 hover:bg-red-100'
                              : 'bg-green-50 text-green-600 border-green-200 hover:bg-green-100'
                          }`}
                        >
                          {isToggling ? (
                            <span className="w-3 h-3 border-2 border-current border-t-transparent rounded-full animate-spin" />
                          ) : svc.active ? (
                            'Deactivate'
                          ) : (
                            'Activate'
                          )}
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
