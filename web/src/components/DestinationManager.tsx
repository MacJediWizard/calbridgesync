import { useState, useEffect } from 'react';
import { getDestinations, createDestination, deleteDestination } from '../services/api';
import type { Destination } from '../types';

interface Props {
  sourceId: string;
}

export default function DestinationManager({ sourceId }: Props) {
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [loading, setLoading] = useState(true);
  const [adding, setAdding] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState({ name: '', dest_url: '', dest_username: '', dest_password: '' });

  useEffect(() => {
    loadDestinations();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceId]);

  const loadDestinations = async () => {
    try {
      const data = await getDestinations(sourceId);
      setDestinations(data);
    } catch {
      setError('Failed to load destinations');
    } finally {
      setLoading(false);
    }
  };

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.dest_url || !form.dest_username || !form.dest_password) {
      setError('All fields are required');
      return;
    }
    setAdding(true);
    setError(null);
    try {
      await createDestination(sourceId, form);
      setForm({ name: '', dest_url: '', dest_username: '', dest_password: '' });
      setShowForm(false);
      await loadDestinations();
    } catch (err: unknown) {
      if (err && typeof err === 'object' && 'response' in err) {
        const axiosErr = err as { response?: { data?: { error?: string } } };
        setError(axiosErr.response?.data?.error || 'Failed to add destination');
      } else {
        setError('Failed to add destination');
      }
    } finally {
      setAdding(false);
    }
  };

  const handleDelete = async (destId: string, name: string) => {
    if (!confirm(`Delete destination "${name || destId}"?`)) return;
    try {
      await deleteDestination(sourceId, destId);
      await loadDestinations();
    } catch {
      setError('Failed to delete destination');
    }
  };

  if (loading) return <p className="text-gray-500 text-sm">Loading destinations...</p>;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-gray-300 uppercase tracking-wide">
          Additional Destinations
        </h3>
        <button
          type="button"
          onClick={() => setShowForm(!showForm)}
          className="px-3 py-1 rounded bg-zinc-700 hover:bg-zinc-600 text-white text-xs font-medium transition-colors"
        >
          {showForm ? 'Cancel' : '+ Add'}
        </button>
      </div>

      {error && <p className="text-sm text-red-400">{error}</p>}

      {destinations.length === 0 && !showForm && (
        <p className="text-xs text-gray-500">No additional destinations. Events sync only to the primary destination above.</p>
      )}

      {destinations.map((dest) => (
        <div key={dest.id} className="flex items-center justify-between p-3 bg-black/50 rounded border border-zinc-700">
          <div className="min-w-0 flex-1">
            <p className="text-sm text-white font-medium truncate">{dest.name || dest.dest_url}</p>
            <p className="text-xs text-gray-500 truncate">{dest.dest_url}</p>
            <p className="text-xs text-gray-500">{dest.dest_username}</p>
          </div>
          <div className="flex items-center space-x-2 ml-3">
            <span className={`text-xs px-1.5 py-0.5 rounded ${dest.enabled ? 'bg-green-900/50 text-green-400' : 'bg-zinc-800 text-gray-500'}`}>
              {dest.enabled ? 'Active' : 'Disabled'}
            </span>
            <button
              type="button"
              onClick={() => handleDelete(dest.id, dest.name)}
              className="text-red-400 hover:text-red-300 text-xs"
            >
              Remove
            </button>
          </div>
        </div>
      ))}

      {showForm && (
        <form onSubmit={handleAdd} className="p-4 bg-black/50 rounded border border-zinc-700 space-y-3">
          <div>
            <label className="block text-xs font-medium text-gray-400 mb-1">Name (optional)</label>
            <input
              type="text"
              value={form.name}
              onChange={(e) => setForm(prev => ({ ...prev, name: e.target.value }))}
              placeholder="e.g. Backup SOGo"
              className="w-full"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-400 mb-1">CalDAV URL</label>
            <input
              type="url"
              value={form.dest_url}
              onChange={(e) => setForm(prev => ({ ...prev, dest_url: e.target.value }))}
              required
              className="w-full"
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium text-gray-400 mb-1">Username</label>
              <input
                type="text"
                value={form.dest_username}
                onChange={(e) => setForm(prev => ({ ...prev, dest_username: e.target.value }))}
                required
                className="w-full"
              />
            </div>
            <div>
              <label className="block text-xs font-medium text-gray-400 mb-1">Password</label>
              <input
                type="password"
                value={form.dest_password}
                onChange={(e) => setForm(prev => ({ ...prev, dest_password: e.target.value }))}
                required
                className="w-full"
              />
            </div>
          </div>
          <div className="flex justify-end">
            <button
              type="submit"
              disabled={adding}
              className="px-4 py-1.5 rounded bg-red-600 hover:bg-red-700 text-white text-sm font-medium transition-colors disabled:opacity-50"
            >
              {adding ? 'Adding...' : 'Add Destination'}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
