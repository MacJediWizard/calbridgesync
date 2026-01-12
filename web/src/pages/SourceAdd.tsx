import { useState } from 'react';
import { useNavigate, Link } from 'react-router-dom';
import { createSource, discoverCalendars } from '../services/api';
import type { SourceFormData, Calendar } from '../types';

export default function SourceAdd() {
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const [calendars, setCalendars] = useState<Calendar[]>([]);
  const [discoverError, setDiscoverError] = useState<string | null>(null);
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
    sync_direction: 'one_way',
    conflict_strategy: 'source_wins',
    selected_calendars: [],
  });

  const handleDiscoverCalendars = async () => {
    if (!form.source_url || !form.source_username || !form.source_password) {
      setDiscoverError('Please fill in source URL, username and password first');
      return;
    }

    setDiscovering(true);
    setDiscoverError(null);
    try {
      const discovered = await discoverCalendars(form.source_url, form.source_username, form.source_password);
      setCalendars(discovered);
      // By default, select all calendars
      setForm(prev => ({ ...prev, selected_calendars: discovered.map(c => c.path) }));
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
    <div className="space-y-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold text-white">Add Calendar Source</h1>
        <p className="text-sm text-gray-400">Configure a new calendar synchronization</p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Form - Left Side */}
        <div className="lg:col-span-2">
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

                {/* Calendar Discovery */}
                <div className="pt-2">
                  <button
                    type="button"
                    onClick={handleDiscoverCalendars}
                    disabled={discovering || !form.source_url || !form.source_username || !form.source_password}
                    className="px-3 py-1.5 rounded bg-zinc-700 hover:bg-zinc-600 text-white text-sm font-medium transition-colors disabled:opacity-50"
                  >
                    {discovering ? 'Discovering...' : 'Discover Calendars'}
                  </button>
                  {discoverError && (
                    <p className="mt-2 text-sm text-red-400">{discoverError}</p>
                  )}
                  {calendars.length > 0 && (
                    <div className="mt-3 p-3 bg-black/50 rounded border border-zinc-700">
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
              <div className="flex justify-end space-x-3 pt-4 border-t border-zinc-800">
                <Link to="/sources" className="px-4 py-2 rounded text-gray-400 hover:text-white text-sm font-medium">
                  Cancel
                </Link>
                <button
                  type="submit"
                  disabled={loading}
                  className="px-4 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors disabled:opacity-50"
                >
                  {loading ? 'Adding...' : 'Add Source'}
                </button>
              </div>
            </form>
          </div>
        </div>

        {/* Help Cards - Right Side */}
        <div className="space-y-4">
          {/* Getting Started */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-2">Getting Started</h3>
            <p className="text-xs text-gray-400 leading-relaxed">
              CalBridgeSync syncs events from a <span className="text-red-400">source</span> calendar to a <span className="text-red-400">destination</span> calendar.
              You'll need CalDAV credentials for both servers.
            </p>
          </div>

          {/* Mailcow / SOGo */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-2">Mailcow / SOGo</h3>
            <p className="text-xs text-gray-400 leading-relaxed mb-2">
              Mailcow uses SOGo for calendars. To find your CalDAV URL:
            </p>
            <ol className="text-xs text-gray-400 list-decimal ml-4 space-y-1 mb-3">
              <li>Log into SOGo webmail</li>
              <li>Click <span className="text-white">Calendar</span> tab</li>
              <li>Right-click your calendar → <span className="text-white">Link to this Calendar</span></li>
              <li>Copy the <span className="text-red-400">CalDAV URL</span></li>
            </ol>
            <div className="bg-black/50 p-2 rounded text-xs">
              <p className="text-gray-500 mb-1">URL format:</p>
              <code className="text-red-400 break-all">https://mail.example.com/SOGo/dav/user@example.com/Calendar/personal/</code>
            </div>
            <p className="text-xs text-gray-500 mt-2">
              <span className="text-white">Username:</span> Your full email address<br/>
              <span className="text-white">Password:</span> Your mailbox password
            </p>
          </div>

          {/* Other CalDAV Servers */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-2">Other CalDAV Servers</h3>
            <div className="space-y-2 text-xs">
              <div className="bg-black/50 p-2 rounded">
                <p className="text-gray-500 mb-1">Nextcloud:</p>
                <code className="text-red-400 break-all">https://cloud.example.com/remote.php/dav/calendars/username/calendar-name/</code>
              </div>
              <div className="bg-black/50 p-2 rounded">
                <p className="text-gray-500 mb-1">Radicale:</p>
                <code className="text-red-400 break-all">https://radicale.example.com/user/calendar.ics/</code>
              </div>
              <div className="bg-black/50 p-2 rounded">
                <p className="text-gray-500 mb-1">Fastmail:</p>
                <code className="text-red-400 break-all">https://caldav.fastmail.com/dav/calendars/user/username/</code>
              </div>
              <div className="bg-black/50 p-2 rounded">
                <p className="text-gray-500 mb-1">iCloud:</p>
                <code className="text-red-400 break-all">https://caldav.icloud.com/</code>
                <p className="text-gray-500 mt-1">Use app-specific password from appleid.apple.com</p>
              </div>
              <div className="bg-black/50 p-2 rounded">
                <p className="text-gray-500 mb-1">Google Calendar:</p>
                <code className="text-red-400 break-all">https://apidata.googleusercontent.com/caldav/v2/calid/events</code>
                <p className="text-gray-500 mt-1">Requires OAuth or app password</p>
              </div>
            </div>
          </div>

          {/* How to Find CalDAV URL */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-2">Finding Your CalDAV URL</h3>
            <p className="text-xs text-gray-400 leading-relaxed mb-2">
              Most calendar apps show the CalDAV URL in settings:
            </p>
            <ul className="text-xs text-gray-400 list-disc ml-4 space-y-1">
              <li>Look for <span className="text-white">"CalDAV"</span>, <span className="text-white">"Sync"</span>, or <span className="text-white">"Subscribe"</span> options</li>
              <li>Check calendar properties or sharing settings</li>
              <li>The URL usually ends with the calendar name or ID</li>
              <li>Make sure to use <span className="text-red-400">https://</span> not http://</li>
            </ul>
          </div>

          {/* Credentials Help */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-2">Credentials</h3>
            <p className="text-xs text-gray-400 leading-relaxed mb-2">
              <span className="text-white">Username:</span> Usually your full email address (e.g., user@example.com)
            </p>
            <p className="text-xs text-gray-400 leading-relaxed mb-2">
              <span className="text-white">Password:</span> Your account password, or an <span className="text-red-400">app-specific password</span> if you have 2FA enabled.
            </p>
            <p className="text-xs text-gray-500">
              For iCloud and Google, you must generate an app-specific password in your account security settings.
            </p>
          </div>

          {/* Sync Settings Help */}
          <div className="bg-zinc-900 rounded-lg border border-zinc-800 p-4">
            <h3 className="text-sm font-semibold text-white mb-2">Sync Settings</h3>
            <div className="space-y-2 text-xs text-gray-400">
              <p><span className="text-white">Interval:</span> How often CalBridgeSync checks for new events.</p>
              <p><span className="text-white">Conflicts:</span> What happens when the same event is modified on both servers:</p>
              <ul className="ml-3 mt-1 space-y-1">
                <li><span className="text-red-400">Source wins</span> - Source changes override destination</li>
                <li><span className="text-red-400">Dest wins</span> - Destination changes are kept</li>
                <li><span className="text-red-400">Newest wins</span> - Most recent change is kept</li>
              </ul>
            </div>
          </div>

          {/* Security Note */}
          <div className="bg-zinc-900 rounded-lg border border-red-900/50 p-4">
            <h3 className="text-sm font-semibold text-red-400 mb-2">Security</h3>
            <p className="text-xs text-gray-400 leading-relaxed">
              All credentials are encrypted with <span className="text-white">AES-256-GCM</span> before
              being stored. Passwords are never logged or exposed in the UI.
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}
