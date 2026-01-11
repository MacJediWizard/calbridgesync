export default function Login() {
  const handleLogin = () => {
    // Redirect to SSO login endpoint
    window.location.href = '/auth/login';
  };

  return (
    <div className="min-h-[80vh] flex items-center justify-center py-8">
      <div className="w-full max-w-sm">
        {/* Logo and Title */}
        <div className="text-center mb-8">
          <img
            src="https://cdn.macjediwizard.com/cdn/CalBridge%20Images/calbridge-06070e03.png"
            alt="CalBridge Logo"
            className="w-24 h-24 mx-auto mb-4"
          />
          <h1 className="text-3xl font-bold text-white" style={{ fontFamily: 'Orbitron, monospace' }}>
            CalBridge
          </h1>
          <p className="mt-2 text-gray-400">CalDAV Calendar Synchronization</p>
        </div>

        {/* Login Card */}
        <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
          <button
            onClick={handleLogin}
            className="w-full py-3 px-4 rounded bg-indigo-600 hover:bg-indigo-700 text-white font-medium transition-colors"
          >
            Sign in with SSO
          </button>
        </div>

        {/* Features */}
        <div className="mt-8 space-y-3">
          <div className="bg-gray-800/50 rounded p-4 border border-gray-700">
            <h3 className="text-sm font-medium text-white">Multi-Calendar Sync</h3>
            <p className="text-xs text-gray-400 mt-1">Sync between any CalDAV servers</p>
          </div>
          <div className="bg-gray-800/50 rounded p-4 border border-gray-700">
            <h3 className="text-sm font-medium text-white">Secure Credentials</h3>
            <p className="text-xs text-gray-400 mt-1">AES-256-GCM encryption at rest</p>
          </div>
          <div className="bg-gray-800/50 rounded p-4 border border-gray-700">
            <h3 className="text-sm font-medium text-white">Automatic Sync</h3>
            <p className="text-xs text-gray-400 mt-1">Configurable sync intervals</p>
          </div>
        </div>
      </div>
    </div>
  );
}
