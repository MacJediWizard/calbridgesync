import { useState, useEffect } from 'react';
import { useNavigate, useParams, Link } from 'react-router-dom';
import { getSource, updateSource, deleteSource, discoverCalendars } from '../services/api';
import type { Source, Calendar } from '../types';

export default function SourceEdit() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [source, setSource] = useState<Source | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const [calendars, setCalendars] = useState<Calendar[]>([]);
  const [discoverError, setDiscoverError] = useState<string | null>(null);
  const [form, setForm] = useState({
    name: '',
    source_type: 'caldav',
    source_url: '',
    source_username: '',
    source_password: '',
    dest_url: '',
    dest_username: '',
    dest_password: '',
    sync_interval: 3600,
    sync_direction: 'one_way' as 'one_way' | 'two_way',
    conflict_strategy: 'source_wins',
    selected_calendars: [] as string[],
  });

  useEffect(() => {
    loadSource();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const loadSource = async () => {
    if (!id) return;
    try {
      const data = await getSource(id);
      setSource(data);
      setForm({
        name: data.name,
        source_type: data.source_type,
        source_url: data.source_url,
        source_username: data.source_username,
        source_password: '',
        dest_url: data.dest_url,
        dest_username: data.dest_username,
        dest_password: '',
        sync_interval: data.sync_interval,
        sync_direction: data.sync_direction || 'one_way',
        conflict_strategy: data.conflict_strategy,
        selected_calendars: data.selected_calendars || [],
      });
    } catch (err) {
      setError('Failed to load source');
      console.error(err);
    } finally {
      setLoading(false);
    }
  };

  const handleDiscoverCalendars = async () => {
    if (!form.source_url || !form.source_username) {
      setDiscoverError('Please fill in source URL and username');
      return;
    }

    // For editing, we need to use the stored password if not changed
    const password = form.source_password || 'USE_EXISTING';
    if (password === 'USE_EXISTING' && !source) {
      setDiscoverError('Please enter password to discover calendars');
      return;
    }

    setDiscovering(true);
    setDiscoverError(null);
    try {
      // If password is empty, we can't discover - need to inform user
      if (!form.source_password) {
        setDiscoverError('Please enter password to discover calendars');
        setDiscovering(false);
        return;
      }
      const discovered = await discoverCalendars(form.source_url, form.source_username, form.source_password);
      setCalendars(discovered);
      // If no calendars selected yet, select all by default
      if (form.selected_calendars.length === 0) {
        setForm(prev => ({ ...prev, selected_calendars: discovered.map(c => c.path) }));
      }
    } catch (err: unknown) {
      if (err && typeof err === 'object' && 'response' in err) {
        const axiosErr = err as { response?: { data?: { error?: string } } };
        setDiscoverError(axiosErr.response?.data?.error || 'Failed to discover calendars');
      } else {
        setDiscoverError('Failed to discover calendars');
      }
    } finally {
      setDiscovering(false);
    }
  };

  const handleCalendarToggle = (path: string) => {
    setForm(prev => {
      const isSelected = prev.selected_calendars.includes(path);
      return {
        ...prev,
        selected_calendars: isSelected
          ? prev.selected_calendars.filter(p => p !== path)
          : [...prev.selected_calendars, path]
      };
    });
  };

  const handleChange = (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
    const { name, value } = e.target;
    setForm((prev) => ({
      ...prev,
      [name]: name === 'sync_interval' ? parseInt(value) : value,
    }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!id) return;
    setSaving(true);
    setError(null);

    try {
      const updateData: Record<string, unknown> = { ...form };
      // Only include passwords if they were changed
      if (!form.source_password) delete updateData.source_password;
      if (!form.dest_password) delete updateData.dest_password;

      await updateSource(id, updateData);
      navigate('/sources');
    } catch (err: unknown) {
      if (err && typeof err === 'object' && 'response' in err) {
        const axiosErr = err as { response?: { data?: { error?: string } } };
        setError(axiosErr.response?.data?.error || 'Failed to update source');
      } else {
        setError('Failed to update source');
      }
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!id) return;
    if (!confirm('Are you sure you want to delete this source?')) return;

    try {
      await deleteSource(id);
      navigate('/sources');
    } catch (err) {
      console.error('Delete failed:', err);
    }
  };

  const formatDate = (dateStr: string | null) => {
    if (!dateStr) return 'Never';
    return new Date(dateStr).toLocaleDateString('en-US', {
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });
  };

  if (loading) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  if (error && !source) {
    return <div className="text-center py-12 text-red-400">{error}</div>;
  }

  return (
    <div className="max-w-2xl mx-auto space-y-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold text-white">Edit Calendar Source</h1>
        <p className="text-sm text-gray-400">Update "{source?.name}"</p>
      </div>

      {/* Form */}
      <div className="bg-zinc-900 rounded-lg border border-zinc-800">
        <form onSubmit={handleSubmit} className="p-6 space-y-6">
          {error && (
            <div className="p-3 rounded bg-red-900/50 border border-red-700 text-red-200 text-sm">{error}</div>
          )}

          {/* General Settings */}
          <div className="space-y-4">
            <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide border-b border-zinc-800 pb-2">
              General
            </h3>
            <div>
              <label htmlFor="name" className="block text-sm font-medium text-gray-300 mb-1">
                Name
              </label>
              <input type="text" name="name" id="name" value={form.name} onChange={handleChange} required className="w-full" />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label htmlFor="source_type" className="block text-sm font-medium text-gray-300 mb-1">
                  Type
                </label>
                <select name="source_type" id="source_type" value={form.source_type} onChange={handleChange} required className="w-full">
                  <option value="caldav">CalDAV</option>
                  <option value="icloud">iCloud</option>
                  <option value="google">Google</option>
                  <option value="outlook">Outlook</option>
                  <option value="fastmail">Fastmail</option>
                  <option value="nextcloud">Nextcloud</option>
                  <option value="custom">Custom</option>
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
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label htmlFor="sync_direction" className="block text-sm font-medium text-gray-300 mb-1">
                  Sync Direction
                </label>
                <select name="sync_direction" id="sync_direction" value={form.sync_direction} onChange={handleChange} required className="w-full">
                  <option value="one_way">One-way (Source to Dest)</option>
                  <option value="two_way">Two-way (Bidirectional)</option>
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
            <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide border-b border-zinc-800 pb-2">
              Source Server
            </h3>
            <div>
              <label htmlFor="source_url" className="block text-sm font-medium text-gray-300 mb-1">
                CalDAV URL
              </label>
              <input type="url" name="source_url" id="source_url" value={form.source_url} onChange={handleChange} required className="w-full" />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label htmlFor="source_username" className="block text-sm font-medium text-gray-300 mb-1">
                  Username
                </label>
                <input type="text" name="source_username" id="source_username" value={form.source_username} onChange={handleChange} required className="w-full" />
              </div>
              <div>
                <label htmlFor="source_password" className="block text-sm font-medium text-gray-300 mb-1">
                  Password
                </label>
                <input type="password" name="source_password" id="source_password" value={form.source_password} onChange={handleChange} placeholder="Leave empty to keep" className="w-full" />
              </div>
            </div>

            {/* Calendar Selection */}
            <div className="pt-2">
              <div className="flex items-center justify-between mb-2">
                <span className="text-sm text-gray-400">
                  {form.selected_calendars.length > 0
                    ? `${form.selected_calendars.length} calendar(s) selected`
                    : 'All calendars (no filter)'}
                </span>
                <button
                  type="button"
                  onClick={handleDiscoverCalendars}
                  disabled={discovering || !form.source_url || !form.source_username}
                  className="px-3 py-1.5 rounded bg-zinc-700 hover:bg-zinc-600 text-white text-sm font-medium transition-colors disabled:opacity-50"
                >
                  {discovering ? 'Discovering...' : 'Discover Calendars'}
                </button>
              </div>
              {discoverError && (
                <p className="text-sm text-red-400 mb-2">{discoverError}</p>
              )}
              {calendars.length > 0 && (
                <div className="p-3 bg-black/50 rounded border border-zinc-700">
                  <p className="text-xs text-gray-400 mb-2">Select calendars to sync:</p>
                  <div className="space-y-2">
                    {calendars.map((cal) => (
                      <label key={cal.path} className="flex items-center space-x-2 cursor-pointer">
                        <input
                          type="checkbox"
                          checked={form.selected_calendars.includes(cal.path)}
                          onChange={() => handleCalendarToggle(cal.path)}
                          className="rounded border-zinc-600 bg-zinc-800 text-red-600 focus:ring-red-500"
                        />
                        <span className="text-sm text-white">{cal.name || cal.path}</span>
                        {cal.color && (
                          <span
                            className="w-3 h-3 rounded-full"
                            style={{ backgroundColor: cal.color }}
                          />
                        )}
                      </label>
                    ))}
                  </div>
                  <p className="text-xs text-gray-500 mt-2">
                    {form.selected_calendars.length} of {calendars.length} selected
                  </p>
                </div>
              )}
              {form.selected_calendars.length > 0 && calendars.length === 0 && (
                <div className="p-2 bg-zinc-800/50 rounded text-xs text-gray-400">
                  Currently syncing {form.selected_calendars.length} calendar(s). Click "Discover Calendars" to modify selection.
                </div>
              )}
            </div>
          </div>

          {/* Destination Server */}
          <div className="space-y-4">
            <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide border-b border-zinc-800 pb-2">
              Destination Server
            </h3>
            <div>
              <label htmlFor="dest_url" className="block text-sm font-medium text-gray-300 mb-1">
                CalDAV URL
              </label>
              <input type="url" name="dest_url" id="dest_url" value={form.dest_url} onChange={handleChange} required className="w-full" />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div>
                <label htmlFor="dest_username" className="block text-sm font-medium text-gray-300 mb-1">
                  Username
                </label>
                <input type="text" name="dest_username" id="dest_username" value={form.dest_username} onChange={handleChange} required className="w-full" />
              </div>
              <div>
                <label htmlFor="dest_password" className="block text-sm font-medium text-gray-300 mb-1">
                  Password
                </label>
                <input type="password" name="dest_password" id="dest_password" value={form.dest_password} onChange={handleChange} placeholder="Leave empty to keep" className="w-full" />
              </div>
            </div>
          </div>

          {/* Status Info */}
          {source && (
            <div className="p-4 rounded bg-black/50 border border-zinc-800">
              <h4 className="text-xs font-semibold text-gray-400 uppercase mb-3">Status</h4>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 text-sm">
                <div>
                  <p className="text-gray-500 text-xs">Enabled</p>
                  <p className="text-white">{source.enabled ? 'Yes' : 'No'}</p>
                </div>
                <div>
                  <p className="text-gray-500 text-xs">Sync Status</p>
                  <p
                    className={
                      source.sync_status === 'success'
                        ? 'text-green-400'
                        : source.sync_status === 'partial'
                        ? 'text-yellow-400'
                        : source.sync_status === 'error'
                        ? 'text-red-400'
                        : 'text-gray-400'
                    }
                  >
                    {source.sync_status === 'success'
                      ? 'OK'
                      : source.sync_status === 'partial'
                      ? 'Partial'
                      : source.sync_status === 'error'
                      ? 'Error'
                      : source.sync_status === 'running'
                      ? 'Running'
                      : 'Pending'}
                  </p>
                </div>
                <div>
                  <p className="text-gray-500 text-xs">Last Sync</p>
                  <p className="text-white">{formatDate(source.last_sync_at)}</p>
                </div>
                <div>
                  <p className="text-gray-500 text-xs">Created</p>
                  <p className="text-white">{formatDate(source.created_at)}</p>
                </div>
              </div>
            </div>
          )}

          {/* Actions */}
          <div className="flex flex-col sm:flex-row justify-between gap-4 pt-4 border-t border-zinc-800">
            <button
              type="button"
              onClick={handleDelete}
              className="px-4 py-2 rounded text-red-400 text-sm font-medium border border-red-700 hover:bg-red-900/30"
            >
              Delete
            </button>
            <div className="flex space-x-3">
              <Link to="/sources" className="px-4 py-2 rounded text-gray-400 hover:text-white text-sm font-medium">
                Cancel
              </Link>
              <button
                type="submit"
                disabled={saving}
                className="px-4 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors disabled:opacity-50"
              >
                {saving ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}
