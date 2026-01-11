import { Link, useLocation, Outlet } from 'react-router-dom';
import type { User } from '../types';

interface LayoutProps {
  user: User | null;
  onLogout: () => void;
}

export default function Layout({ user, onLogout }: LayoutProps) {
  const location = useLocation();

  const isActive = (path: string) => {
    if (path === '/') return location.pathname === '/';
    return location.pathname.startsWith(path);
  };

  return (
    <div className="min-h-screen flex flex-col">
      {/* Navigation */}
      <nav className="bg-gray-800 border-b border-gray-700">
        <div className="max-w-6xl mx-auto px-4">
          <div className="flex items-center justify-between h-14">
            <div className="flex items-center">
              <Link to="/" className="text-lg font-bold text-white" style={{ fontFamily: 'Orbitron, monospace' }}>
                CalBridge
              </Link>
              {user && (
                <div className="hidden md:flex ml-8 space-x-1">
                  <Link
                    to="/"
                    className={`px-3 py-2 rounded text-sm ${
                      isActive('/') && location.pathname === '/'
                        ? 'bg-gray-700 text-white'
                        : 'text-gray-300 hover:text-white hover:bg-gray-700'
                    }`}
                  >
                    Dashboard
                  </Link>
                  <Link
                    to="/sources"
                    className={`px-3 py-2 rounded text-sm ${
                      isActive('/sources')
                        ? 'bg-gray-700 text-white'
                        : 'text-gray-300 hover:text-white hover:bg-gray-700'
                    }`}
                  >
                    Sources
                  </Link>
                </div>
              )}
            </div>
            {user && (
              <div className="flex items-center space-x-4">
                <span className="hidden sm:block text-sm text-gray-400">{user.email}</span>
                <button
                  onClick={onLogout}
                  className="px-3 py-1.5 text-sm text-gray-300 hover:text-white"
                >
                  Logout
                </button>
              </div>
            )}
          </div>
        </div>
      </nav>

      {/* Main Content */}
      <main className="flex-1 max-w-6xl mx-auto w-full py-6 px-4">
        <Outlet />
      </main>

      {/* Footer */}
      <footer className="bg-gray-800 border-t border-gray-700 py-4 mt-auto">
        <div className="max-w-6xl mx-auto px-4 text-center">
          <p className="text-gray-500 text-sm">CalBridge - CalDAV Synchronization</p>
          <p className="text-gray-600 text-xs mt-1">Powered by MacJediWizard Digital Wizardry</p>
        </div>
      </footer>
    </div>
  );
}
