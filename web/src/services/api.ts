import axios from 'axios';
import type { Source, SyncLog, DashboardStats, SourceFormData, AuthStatus } from '../types';

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

export default api;
