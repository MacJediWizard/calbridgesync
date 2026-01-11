import { useState } from 'react';
import { useNavigate, Link } from 'react-router-dom';
import { createSource } from '../services/api';
import type { SourceFormData } from '../types';

export default function SourceAdd() {
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<SourceFormData>({
    name: '',
    source_type: 'caldav',
    source_url: '',
    source_username: '',
    source_password: '',
    dest_url: '',
    dest_username: '',
    dest_password: '',
    sync_interval: 3600,
    conflict_strategy: 'source_wins',
  });

  const handleChange = (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
    const { name, value } = e.target;
    setForm((prev) => ({
      ...prev,
      [name]: name === 'sync_interval' ? parseInt(value) : value,
    }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError(null);

    try {
      await createSource(form);
      navigate('/sources');
    } catch (err: unknown) {
      if (err && typeof err === 'object' && 'response' in err) {
        const axiosErr = err as { response?: { data?: { error?: string } } };
        setError(axiosErr.response?.data?.error || 'Failed to create source');
      } else {
        setError('Failed to create source');
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="max-w-2xl mx-auto space-y-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold text-white">Add Calendar Source</h1>
        <p className="text-sm text-gray-400">Configure a new calendar synchronization</p>
      </div>

      {/* Form */}
      <div className="bg-gray-800 rounded-lg border border-gray-700">
        <form onSubmit={handleSubmit} className="p-6 space-y-6">
          {error && (
            <div className="p-3 rounded bg-red-900/50 border border-red-700 text-red-200 text-sm">{error}</div>
          )}

          {/* General Settings */}
          <div className="space-y-4">
            <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide border-b border-gray-700 pb-2">
              General
            </h3>
            <div>
              <label htmlFor="name" className="block text-sm font-medium text-gray-300 mb-1">
                Name
              </label>
              <input
                type="text"
                name="name"
                id="name"
                value={form.name}
                onChange={handleChange}
                required
                placeholder="My Calendar Sync"
                className="w-full"
              />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
              <div>
                <label htmlFor="source_type" className="block text-sm font-medium text-gray-300 mb-1">
                  Type
                </label>
                <select name="source_type" id="source_type" value={form.source_type} onChange={handleChange} required className="w-full">
                  <option value="caldav">CalDAV</option>
                  <option value="google">Google</option>
                  <option value="outlook">Outlook</option>
                </select>
              </div>
              <div>
                <label htmlFor="sync_interval" className="block text-sm font-medium text-gray-300 mb-1">
                  Interval
                </label>
                <select name="sync_interval" id="sync_interval" value={form.sync_interval} onChange={handleChange} required className="w-full">
                  <option value={300}>5 min</option>
                  <option value={900}>15 min</option>
                  <option value={1800}>30 min</option>
                  <option value={3600}>1 hour</option>
                  <option value={7200}>2 hours</option>
                  <option value={21600}>6 hours</option>
                  <option value={86400}>24 hours</option>
                </select>
              </div>
              <div>
                <label htmlFor="conflict_strategy" className="block text-sm font-medium text-gray-300 mb-1">
                  Conflicts
                </label>
                <select name="conflict_strategy" id="conflict_strategy" value={form.conflict_strategy} onChange={handleChange} required className="w-full">
                  <option value="source_wins">Source wins</option>
                  <option value="dest_wins">Dest wins</option>
                  <option value="newest_wins">Newest wins</option>
                </select>
              </div>
            </div>
          </div>

          {/* Source Server */}
          <div className="space-y-4">
            <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide border-b border-gray-700 pb-2">
              Source Server
            </h3>
            <div>
              <label htmlFor="source_url" className="block text-sm font-medium text-gray-300 mb-1">
                CalDAV URL
              </label>
              <input
                type="url"
                name="source_url"
                id="source_url"
                value={form.source_url}
                onChange={handleChange}
                required
                placeholder="https://caldav.example.com/calendars/user/"
                className="w-full"
              />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label htmlFor="source_username" className="block text-sm font-medium text-gray-300 mb-1">
                  Username
                </label>
                <input
                  type="text"
                  name="source_username"
                  id="source_username"
                  value={form.source_username}
                  onChange={handleChange}
                  required
                  placeholder="user@example.com"
                  className="w-full"
                />
              </div>
              <div>
                <label htmlFor="source_password" className="block text-sm font-medium text-gray-300 mb-1">
                  Password
                </label>
                <input
                  type="password"
                  name="source_password"
                  id="source_password"
                  value={form.source_password}
                  onChange={handleChange}
                  required
                  className="w-full"
                />
              </div>
            </div>
          </div>

          {/* Destination Server */}
          <div className="space-y-4">
            <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide border-b border-gray-700 pb-2">
              Destination Server
            </h3>
            <div>
              <label htmlFor="dest_url" className="block text-sm font-medium text-gray-300 mb-1">
                CalDAV URL
              </label>
              <input
                type="url"
                name="dest_url"
                id="dest_url"
                value={form.dest_url}
                onChange={handleChange}
                required
                placeholder="https://caldav.example.com/calendars/user/"
                className="w-full"
              />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label htmlFor="dest_username" className="block text-sm font-medium text-gray-300 mb-1">
                  Username
                </label>
                <input
                  type="text"
                  name="dest_username"
                  id="dest_username"
                  value={form.dest_username}
                  onChange={handleChange}
                  required
                  placeholder="user@example.com"
                  className="w-full"
                />
              </div>
              <div>
                <label htmlFor="dest_password" className="block text-sm font-medium text-gray-300 mb-1">
                  Password
                </label>
                <input
                  type="password"
                  name="dest_password"
                  id="dest_password"
                  value={form.dest_password}
                  onChange={handleChange}
                  required
                  className="w-full"
                />
              </div>
            </div>
            <p className="text-xs text-gray-500">Passwords are encrypted with AES-256-GCM</p>
          </div>

          {/* Actions */}
          <div className="flex justify-end space-x-3 pt-4 border-t border-gray-700">
            <Link to="/sources" className="px-4 py-2 rounded text-gray-400 hover:text-white text-sm font-medium">
              Cancel
            </Link>
            <button
              type="submit"
              disabled={loading}
              className="px-4 py-2 rounded bg-indigo-600 hover:bg-indigo-700 text-white text-sm font-medium transition-colors disabled:opacity-50"
            >
              {loading ? 'Adding...' : 'Add Source'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
