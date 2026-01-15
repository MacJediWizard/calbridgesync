import { useState, useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { getAuthStatus, logout } from './services/api';
import type { User } from './types';

import Layout from './components/Layout';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Activity from './pages/Activity';
import SourcesList from './pages/SourcesList';
import SourceAdd from './pages/SourceAdd';
import SourceEdit from './pages/SourceEdit';
import SourceLogs from './pages/SourceLogs';
import Settings from './pages/Settings';

function App() {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    checkAuth();
  }, []);

  const checkAuth = async () => {
    try {
      const status = await getAuthStatus();
      if (status.authenticated && status.user) {
        setUser(status.user);
      }
    } catch {
      // Not authenticated
      console.log('Not authenticated');
    } finally {
      setLoading(false);
    }
  };

  const handleLogout = async () => {
    try {
      await logout();
      setUser(null);
      window.location.href = '/';
    } catch (err) {
      console.error('Logout failed:', err);
    }
  };

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="text-gray-400">Loading...</div>
      </div>
    );
  }

  return (
    <BrowserRouter>
      <Routes>
        {!user ? (
          <>
            <Route path="/login" element={<Login />} />
            <Route path="*" element={<Navigate to="/login" replace />} />
          </>
        ) : (
          <Route element={<Layout user={user} onLogout={handleLogout} />}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/activity" element={<Activity />} />
            <Route path="/sources" element={<SourcesList />} />
            <Route path="/sources/add" element={<SourceAdd />} />
            <Route path="/sources/:id/edit" element={<SourceEdit />} />
            <Route path="/sources/:id/logs" element={<SourceLogs />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        )}
      </Routes>
    </BrowserRouter>
  );
}

export default App;
