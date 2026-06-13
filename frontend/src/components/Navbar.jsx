export default function Navbar({ currentPage, onNavigate }) {
  const links = [
    { id: 'containers', label: 'Containers' },
    { id: 'wakeproxy', label: 'WakeProxy' },
  ]

  return (
    <nav className="flex items-center gap-1">
      {links.map((link) => {
        const active = currentPage === link.id
        return (
          <button
            key={link.id}
            onClick={() => onNavigate(link.id)}
            className={`px-3 py-1.5 rounded text-xs font-semibold border cursor-pointer transition-colors ${
              active
                ? 'bg-gray-900 text-white border-gray-900'
                : 'bg-white text-gray-500 border-gray-200 hover:border-gray-400 hover:text-gray-700'
            }`}
          >
            {link.label}
          </button>
        )
      })}
    </nav>
  )
}
