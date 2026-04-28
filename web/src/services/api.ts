import axios from 'axios';
import type { Source, SyncLog, DashboardStats, SourceFormData, AuthStatus, SyncHistory, MalformedEvent, Calendar, AlertPreferences, ActivityData, Destination } from '../types';

const api = axios.create({
  baseURL: '/api',
  headers: {
    'Content-Type': 'application/json',
  },
});

// Auth
export const getAuthStatus = async (): Promise<AuthStatus> => {
  const response = await api.get('/auth/status');
  return response.data;
};

export const logout = async (): Promise<void> => {
  await api.post('/auth/logout');
};

export const getVersion = async (): Promise<{ version: string }> => {
  const response = await api.get('/version');
  return response.data;
};

// Dashboard
export const getDashboardStats = async (): Promise<DashboardStats> => {
  const response = await api.get('/dashboard/stats');
  return response.data;
};

export const getSyncHistory = async (days: number = 7): Promise<SyncHistory> => {
  const response = await api.get('/dashboard/sync-history', { params: { days } });
  return response.data;
};

// Sources
export const getSources = async (): Promise<Source[]> => {
  const response = await api.get('/sources');
  return response.data;
};

export const getSource = async (id: string): Promise<Source> => {
  const response = await api.get(`/sources/${id}`);
  return response.data;
};

export const createSource = async (data: SourceFormData): Promise<Source> => {
  const response = await api.post('/sources', data);
  return response.data;
};

// Google OAuth2 source preparation (#70).
// Called when the user picks source_type=google in the add-source
// form. The backend validates the form, tests the destination,
// stashes the encrypted form in a session cookie, and returns a URL
// to redirect to Google's consent screen. After the user approves at
// Google, the backend callback creates the real source and
// redirects back to /sources.
export interface PrepareGoogleSourceRequest {
  name: string;
  sync_interval: number;
  sync_days_past: number;
  sync_direction: string;
  conflict_strategy: string;
  dest_url: string;
  dest_username: string;
  dest_password: string;
  strip_alarms: boolean;
  // Per-source Google OAuth credentials (#79). The user provides
  // their own Google Cloud project client_id + client_secret instead
  // of relying on a global env-var configured value.
  google_client_id: string;
  google_client_secret: string;
}

export interface PrepareGoogleSourceResponse {
  redirect_url: string;
}

export const prepareGoogleSource = async (data: PrepareGoogleSourceRequest): Promise<PrepareGoogleSourceResponse> => {
  const response = await api.post('/sources/google/prepare', data);
  return response.data;
};

export const updateSource = async (id: string, data: Partial<SourceFormData>): Promise<Source> => {
  const response = await api.put(`/sources/${id}`, data);
  return response.data;
};

export const deleteSource = async (id: string): Promise<void> => {
  await api.delete(`/sources/${id}`);
};

export const toggleSource = async (id: string): Promise<Source> => {
  const response = await api.post(`/sources/${id}/toggle`);
  return response.data;
};

export const triggerSync = async (id: string): Promise<void> => {
  await api.post(`/sources/${id}/sync`);
};

export const dryRunSync = async (id: string): Promise<{
  success: boolean;
  created: number;
  updated: number;
  deleted: number;
  skipped: number;
  dry_run: boolean;
  message: string;
  warnings?: string[];
}> => {
  const response = await api.post(`/sources/${id}/sync?dry_run=true`);
  return response.data;
};

export const getSourceStats = async (id: string): Promise<{
  synced_event_count: number;
  malformed_count: number;
  success_rate: number;
  health_score: number;
  health_label: string;
  recent_syncs: { status: string; duration_ms: number; created_at: string }[];
}> => {
  const response = await api.get(`/sources/${id}/stats`);
  return response.data;
};

// Logs
export const getSourceLogs = async (sourceId: string, page: number = 1): Promise<{ logs: SyncLog[]; total_pages: number; page: number }> => {
  const response = await api.get(`/sources/${sourceId}/logs`, { params: { page } });
  return response.data;
};

// Malformed Events
export const getMalformedEvents = async (): Promise<MalformedEvent[]> => {
  const response = await api.get('/malformed-events');
  return response.data;
};

export const deleteMalformedEvent = async (id: string): Promise<void> => {
  await api.delete(`/malformed-events/${id}`);
};

export const deleteAllMalformedEvents = async (): Promise<{ deleted: number }> => {
  const response = await api.delete('/malformed-events');
  return response.data;
};

// Calendar Discovery
export const discoverCalendars = async (url: string, username: string, password: string): Promise<Calendar[]> => {
  const response = await api.post('/calendars/discover', { url, username, password });
  return response.data;
};

// Alert Preferences
export const getAlertPreferences = async (): Promise<AlertPreferences> => {
  const response = await api.get('/settings/alerts');
  return response.data;
};

export const updateAlertPreferences = async (data: Partial<AlertPreferences>): Promise<AlertPreferences> => {
  const response = await api.put('/settings/alerts', data);
  return response.data;
};

export const testWebhook = async (url: string): Promise<void> => {
  await api.post('/settings/alerts/test-webhook', { webhook_url: url });
};

export const getAuditLogs = async (page: number = 1): Promise<{ logs: { id: string; action: string; resource_type: string; resource_id: string; details: string; ip_address: string; created_at: string }[]; total_pages: number }> => {
  const response = await api.get(`/audit-logs?page=${page}`);
  return response.data;
};

export const exportCalendars = async (): Promise<Blob> => {
  const response = await api.get('/export/calendars', { responseType: 'blob' });
  return response.data;
};

export const getLogStats = async (): Promise<{ total_logs: number; oldest_log: string; retention_days: number }> => {
  const response = await api.get('/settings/log-stats');
  return response.data;
};

// Activity
export const getActivity = async (): Promise<ActivityData> => {
  const response = await api.get('/activity');
  return response.data;
};

// Destinations (multi-destination sync #156)
export const getDestinations = async (sourceId: string): Promise<Destination[]> => {
  const response = await api.get(`/sources/${sourceId}/destinations`);
  return response.data;
};

export const createDestination = async (sourceId: string, data: { name: string; dest_url: string; dest_username: string; dest_password: string }): Promise<Destination> => {
  const response = await api.post(`/sources/${sourceId}/destinations`, data);
  return response.data;
};

export const deleteDestination = async (sourceId: string, destId: string): Promise<void> => {
  await api.delete(`/sources/${sourceId}/destinations/${destId}`);
};

export default api;
