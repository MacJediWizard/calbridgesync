import { useState } from 'react';

const LOGO_URL = 'https://cdn.macjediwizard.com/cdn/CalBridge%20Images/calbridge-06070e03.png';

export default function Login() {
  const [logoError, setLogoError] = useState(false);

  const handleLogin = () => {
    window.location.href = '/auth/login';
  };

  return (
    <div className="min-h-screen flex items-center justify-center py-8 bg-black">
      <div className="w-full max-w-sm px-4">
        {/* Logo and Title */}
        <div className="text-center mb-8">
          {!logoError && (
            <img
              src={LOGO_URL}
              alt="CalBridge Logo"
              className="w-32 h-32 mx-auto mb-6 object-contain"
              onError={() => setLogoError(true)}
            />
          )}
          {logoError && (
            <div className="w-32 h-32 mx-auto mb-6 rounded-full bg-red-600 flex items-center justify-center">
              <span className="text-4xl font-bold text-white" style={{ fontFamily: 'Orbitron, monospace' }}>CB</span>
            </div>
          )}
          <h1 className="text-3xl font-bold text-white" style={{ fontFamily: 'Orbitron, monospace' }}>
            CalBridge
          </h1>
          <p className="mt-2 text-gray-400">CalDAV Calendar Synchronization</p>
        </div>

        {/* Login Card */}
        <div className="bg-zinc-900 rounded-lg p-6 border border-zinc-800">
          <button
            onClick={handleLogin}
            className="w-full py-3 px-4 rounded bg-red-600 hover:bg-red-700 text-white font-medium transition-colors"
          >
            Sign in with SSO
          </button>
        </div>

        {/* Features */}
        <div className="mt-8 space-y-3">
          <div className="bg-zinc-900 rounded p-4 border border-zinc-800">
            <h3 className="text-sm font-medium text-white">Multi-Calendar Sync</h3>
            <p className="text-xs text-gray-400 mt-1">Sync between any CalDAV servers</p>
          </div>
          <div className="bg-zinc-900 rounded p-4 border border-zinc-800">
            <h3 className="text-sm font-medium text-white">Secure Credentials</h3>
            <p className="text-xs text-gray-400 mt-1">AES-256-GCM encryption at rest</p>
          </div>
          <div className="bg-zinc-900 rounded p-4 border border-zinc-800">
            <h3 className="text-sm font-medium text-white">Automatic Sync</h3>
            <p className="text-xs text-gray-400 mt-1">Configurable sync intervals</p>
          </div>
        </div>

        {/* Footer */}
        <div className="mt-8 text-center">
          <p className="text-xs text-gray-600">Powered by MacJediWizard Digital Wizardry</p>
        </div>
      </div>
    </div>
  );
}
