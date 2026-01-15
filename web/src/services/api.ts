import axios from 'axios';
import type { Source, SyncLog, DashboardStats, SourceFormData, AuthStatus, SyncHistory, MalformedEvent, Calendar, AlertPreferences } from '../types';

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

export default api;
