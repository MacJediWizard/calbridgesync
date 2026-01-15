import { useState, useEffect } from 'react';
import { getAlertPreferences, updateAlertPreferences, testWebhook } from '../services/api';
import type { AlertPreferences } from '../types';

export default function Settings() {
  const [preferences, setPreferences] = useState<AlertPreferences>({
    email_enabled: null,
    webhook_enabled: null,
    webhook_url: '',
    cooldown_minutes: null,
  });
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  useEffect(() => {
    loadPreferences();
  }, []);

  const loadPreferences = async () => {
    try {
      const data = await getAlertPreferences();
      setPreferences(data);
    } catch (err) {
      setError('Failed to load preferences');
      console.error(err);
    } finally {
      setLoading(false);
    }
  };

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    setSuccess(null);

    try {
      const updated = await updateAlertPreferences(preferences);
      setPreferences(updated);
      setSuccess('Preferences saved successfully');
      setTimeout(() => setSuccess(null), 3000);
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message :
        (err && typeof err === 'object' && 'response' in err &&
         err.response && typeof err.response === 'object' && 'data' in err.response &&
         err.response.data && typeof err.response.data === 'object' && 'error' in err.response.data)
          ? String(err.response.data.error)
          : 'Failed to save preferences';
      setError(errorMessage);
    } finally {
      setSaving(false);
    }
  };

  const handleTestWebhook = async () => {
    if (!preferences.webhook_url) {
      setError('Please enter a webhook URL first');
      return;
    }

    setTesting(true);
    setError(null);
    setSuccess(null);

    try {
      await testWebhook(preferences.webhook_url);
      setSuccess('Test webhook sent successfully');
      setTimeout(() => setSuccess(null), 3000);
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message :
        (err && typeof err === 'object' && 'response' in err &&
         err.response && typeof err.response === 'object' && 'data' in err.response &&
         err.response.data && typeof err.response.data === 'object' && 'error' in err.response.data)
          ? String(err.response.data.error)
          : 'Failed to send test webhook';
      setError(errorMessage);
    } finally {
      setTesting(false);
    }
  };

  if (loading) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  return (
    <div className="max-w-2xl mx-auto space-y-6">
      {/* Header */}
      <div>
        <h1 className="text-2xl font-bold text-white">Settings</h1>
        <p className="text-sm text-gray-400">Configure your alert preferences</p>
      </div>

      {/* Alert Messages */}
      {error && (
        <div className="bg-red-900/30 border border-red-900/50 rounded-lg px-4 py-3 text-red-400 text-sm">
          {error}
        </div>
      )}
      {success && (
        <div className="bg-green-900/30 border border-green-900/50 rounded-lg px-4 py-3 text-green-400 text-sm">
          {success}
        </div>
      )}

      {/* Alert Preferences */}
      <div className="bg-zinc-900 rounded-lg border border-zinc-800 overflow-hidden">
        <div className="px-4 py-3 border-b border-zinc-800">
          <h2 className="text-sm font-semibold text-white">Alert Notifications</h2>
          <p className="text-xs text-gray-500 mt-1">
            Receive notifications when your calendar sources become stale or recover
          </p>
        </div>
        <div className="p-4 space-y-6">
          {/* Email Alerts */}
          <div className="flex items-center justify-between">
            <div>
              <label className="text-sm font-medium text-white">Email Alerts</label>
              <p className="text-xs text-gray-500 mt-1">Receive alerts via email to your account email</p>
            </div>
            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={preferences.email_enabled ?? false}
                onChange={(e) => setPreferences({ ...preferences, email_enabled: e.target.checked })}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-zinc-700 peer-focus:outline-none peer-focus:ring-2 peer-focus:ring-red-500 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-red-600"></div>
            </label>
          </div>

          {/* Webhook Alerts */}
          <div className="flex items-center justify-between">
            <div>
              <label className="text-sm font-medium text-white">Webhook Alerts</label>
              <p className="text-xs text-gray-500 mt-1">Send alerts to a webhook URL (Slack, Discord, etc.)</p>
            </div>
            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={preferences.webhook_enabled ?? false}
                onChange={(e) => setPreferences({ ...preferences, webhook_enabled: e.target.checked })}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-zinc-700 peer-focus:outline-none peer-focus:ring-2 peer-focus:ring-red-500 rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-red-600"></div>
            </label>
          </div>

          {/* Webhook URL */}
          <div>
            <label className="text-sm font-medium text-white">Webhook URL</label>
            <p className="text-xs text-gray-500 mt-1 mb-2">
              Enter your Slack, Discord, or custom webhook URL (must use HTTPS)
            </p>
            <div className="flex gap-2">
              <input
                type="url"
                value={preferences.webhook_url}
                onChange={(e) => setPreferences({ ...preferences, webhook_url: e.target.value })}
                placeholder="https://hooks.slack.com/services/..."
                className="flex-1 bg-zinc-800 border border-zinc-700 rounded px-3 py-2 text-white text-sm placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-red-500 focus:border-transparent"
              />
              <button
                onClick={handleTestWebhook}
                disabled={testing || !preferences.webhook_url}
                className="px-4 py-2 rounded bg-zinc-700 hover:bg-zinc-600 text-white text-sm font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {testing ? 'Sending...' : 'Test'}
              </button>
            </div>
          </div>

          {/* Cooldown */}
          <div>
            <label className="text-sm font-medium text-white">Alert Cooldown</label>
            <p className="text-xs text-gray-500 mt-1 mb-2">
              Minimum time between repeated alerts for the same source
            </p>
            <select
              value={preferences.cooldown_minutes ?? ''}
              onChange={(e) => setPreferences({
                ...preferences,
                cooldown_minutes: e.target.value ? parseInt(e.target.value, 10) : null
              })}
              className="bg-zinc-800 border border-zinc-700 rounded px-3 py-2 text-white text-sm focus:outline-none focus:ring-2 focus:ring-red-500 focus:border-transparent"
            >
              <option value="">Use default</option>
              <option value="5">5 minutes</option>
              <option value="15">15 minutes</option>
              <option value="30">30 minutes</option>
              <option value="60">1 hour</option>
              <option value="120">2 hours</option>
              <option value="360">6 hours</option>
              <option value="720">12 hours</option>
              <option value="1440">24 hours</option>
            </select>
          </div>
        </div>
      </div>

      {/* Save Button */}
      <div className="flex justify-end">
        <button
          onClick={handleSave}
          disabled={saving}
          className="px-6 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {saving ? 'Saving...' : 'Save Preferences'}
        </button>
      </div>
    </div>
  );
}
