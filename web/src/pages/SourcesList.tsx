import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import { getSources, toggleSource, deleteSource, triggerSync } from '../services/api';
import type { Source } from '../types';

export default function SourcesList() {
  const [sources, setSources] = useState<Source[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionId, setActionId] = useState<string | null>(null);

  useEffect(() => {
    loadSources();
  }, []);

  const loadSources = async () => {
    try {
      const data = await getSources();
      setSources(data);
    } catch (err) {
      setError('Failed to load sources');
      console.error(err);
    } finally {
      setLoading(false);
    }
  };

  const handleToggle = async (id: string) => {
    setActionId(id);
    try {
      const updated = await toggleSource(id);
      setSources(sources.map((s) => (s.id === id ? updated : s)));
    } catch (err) {
      console.error('Toggle failed:', err);
    } finally {
      setActionId(null);
    }
  };

  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to delete this source?')) return;
    setActionId(id);
    try {
      await deleteSource(id);
      setSources(sources.filter((s) => s.id !== id));
    } catch (err) {
      console.error('Delete failed:', err);
    } finally {
      setActionId(null);
    }
  };

  const handleSync = async (id: string) => {
    setActionId(id);
    try {
      await triggerSync(id);
      await loadSources();
    } catch (err) {
      console.error('Sync failed:', err);
    } finally {
      setActionId(null);
    }
  };

  const formatInterval = (seconds: number) => {
    if (seconds >= 3600) return `${Math.floor(seconds / 3600)}h`;
    if (seconds >= 60) return `${Math.floor(seconds / 60)}m`;
    return `${seconds}s`;
  };

  if (loading) {
    return <div className="text-center py-12 text-gray-400">Loading...</div>;
  }

  if (error) {
    return <div className="text-center py-12 text-red-400">{error}</div>;
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-white">Calendar Sources</h1>
          <p className="text-sm text-gray-400">Manage your sync configurations</p>
        </div>
        <Link
          to="/sources/add"
          className="inline-flex items-center px-4 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors"
        >
          + Add Source
        </Link>
      </div>

      {/* Sources Table */}
      <div className="bg-zinc-900 rounded-lg border border-zinc-800 overflow-hidden">
        {sources.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-black/50">
                <tr>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Name</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Source</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Destination</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Interval</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Status</th>
                  <th className="px-4 py-3 text-left text-xs font-medium text-gray-400 uppercase">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800">
                {sources.map((source) => (
                  <tr key={source.id} className="hover:bg-zinc-800/50">
                    <td className="px-4 py-3">
                      <div className="font-medium text-white">{source.name}</div>
                      <div className="text-xs text-gray-500">{source.source_type}</div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="text-xs text-gray-300 truncate max-w-[180px]" title={source.source_url}>
                        {source.source_url}
                      </div>
                      <div className="text-xs text-gray-500">{source.source_username}</div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="text-xs text-gray-300 truncate max-w-[180px]" title={source.dest_url}>
                        {source.dest_url}
                      </div>
                      <div className="text-xs text-gray-500">{source.dest_username}</div>
                    </td>
                    <td className="px-4 py-3 text-gray-400 text-xs">{formatInterval(source.sync_interval)}</td>
                    <td className="px-4 py-3">
                      <div className="flex items-center space-x-2">
                        <button
                          onClick={() => handleToggle(source.id)}
                          disabled={actionId === source.id}
                          className={`relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors ${
                            source.enabled ? 'bg-red-600' : 'bg-gray-600'
                          }`}
                        >
                          <span
                            className={`pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow transition ${
                              source.enabled ? 'translate-x-4' : 'translate-x-0'
                            }`}
                          />
                        </button>
                        {source.enabled && (
                          <span
                            className={`text-xs ${
                              source.sync_status === 'success'
                                ? 'text-green-400'
                                : source.sync_status === 'error'
                                ? 'text-red-400'
                                : source.sync_status === 'running'
                                ? 'text-yellow-400'
                                : 'text-gray-500'
                            }`}
                          >
                            {source.sync_status === 'success'
                              ? 'OK'
                              : source.sync_status === 'error'
                              ? 'Err'
                              : source.sync_status === 'running'
                              ? '...'
                              : '-'}
                          </span>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center space-x-3">
                        <button
                          onClick={() => handleSync(source.id)}
                          disabled={!source.enabled || actionId === source.id}
                          className="text-red-400 hover:text-red-300 text-xs font-medium disabled:opacity-50"
                        >
                          Sync
                        </button>
                        <Link
                          to={`/sources/${source.id}/edit`}
                          className="text-gray-400 hover:text-white text-xs font-medium"
                        >
                          Edit
                        </Link>
                        <Link
                          to={`/sources/${source.id}/logs`}
                          className="text-gray-400 hover:text-white text-xs font-medium"
                        >
                          Logs
                        </Link>
                        <button
                          onClick={() => handleDelete(source.id)}
                          disabled={actionId === source.id}
                          className="text-red-400 hover:text-red-300 text-xs font-medium disabled:opacity-50"
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="text-center py-12 px-4">
            <h3 className="text-base font-medium text-white">No calendar sources yet</h3>
            <p className="mt-1 text-sm text-gray-400">Add your first source to begin syncing.</p>
            <Link
              to="/sources/add"
              className="mt-4 inline-flex items-center px-4 py-2 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors"
            >
              + Add Your First Source
            </Link>
          </div>
        )}
      </div>
    </div>
  );
}
