import { useState } from 'react';
import { Link, useLocation, Outlet } from 'react-router-dom';
import type { User } from '../types';

const LOGO_URL = 'https://cdn.macjediwizard.com/cdn/CalBridge%20Images/calbridge-06070e03.png';

interface LayoutProps {
  user: User | null;
  onLogout: () => void;
}

export default function Layout({ user, onLogout }: LayoutProps) {
  const location = useLocation();
  const [logoError, setLogoError] = useState(false);

  const isActive = (path: string) => {
    if (path === '/') return location.pathname === '/';
    return location.pathname.startsWith(path);
  };

  return (
    <div className="min-h-screen flex flex-col bg-black">
      {/* Navigation */}
      <nav className="bg-zinc-900 border-b border-zinc-800">
        <div className="max-w-6xl mx-auto px-4">
          <div className="flex items-center justify-between h-16">
            <div className="flex items-center">
              <Link to="/" className="flex items-center space-x-3">
                {!logoError ? (
                  <img
                    src={LOGO_URL}
                    alt="CalBridge"
                    className="w-10 h-10 object-contain"
                    onError={() => setLogoError(true)}
                  />
                ) : (
                  <div className="w-10 h-10 rounded-full bg-red-600 flex items-center justify-center">
                    <span className="text-sm font-bold text-white">CB</span>
                  </div>
                )}
                <span className="text-lg font-bold text-white" style={{ fontFamily: 'Orbitron, monospace' }}>
                  CalBridge
                </span>
              </Link>
              {user && (
                <div className="hidden md:flex ml-8 space-x-1">
                  <Link
                    to="/"
                    className={`px-3 py-2 rounded text-sm ${
                      isActive('/') && location.pathname === '/'
                        ? 'bg-red-600 text-white'
                        : 'text-gray-300 hover:text-white hover:bg-zinc-800'
                    }`}
                  >
                    Dashboard
                  </Link>
                  <Link
                    to="/sources"
                    className={`px-3 py-2 rounded text-sm ${
                      isActive('/sources')
                        ? 'bg-red-600 text-white'
                        : 'text-gray-300 hover:text-white hover:bg-zinc-800'
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
                  className="px-3 py-1.5 text-sm text-gray-300 hover:text-white hover:bg-zinc-800 rounded"
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
      <footer className="bg-zinc-900 border-t border-zinc-800 py-4 mt-auto">
        <div className="max-w-6xl mx-auto px-4 text-center">
          <p className="text-gray-500 text-sm">CalBridge - CalDAV Synchronization</p>
          <p className="text-gray-600 text-xs mt-1">Powered by MacJediWizard Digital Wizardry</p>
        </div>
      </footer>
    </div>
  );
}
