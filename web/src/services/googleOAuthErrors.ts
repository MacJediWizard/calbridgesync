// Human-readable mapping for the error query params the backend
// Google OAuth callback redirects back with when something goes
// wrong. Shared by the add-source page and the reconnect flow on the
// edit page. Keep this in sync with internal/web/oauth_google.go.
// (#70, #192)
export const GOOGLE_OAUTH_ERRORS: Record<string, string> = {
  google_not_configured: 'Google OAuth is not configured on this server. The instance redirect URL must be set (BASE_URL or GOOGLE_OAUTH_REDIRECT_URL).',
  invalid_state: 'OAuth state mismatch. Please start the flow again.',
  google_denied: 'You denied the Google consent request.',
  missing_code: 'Google did not return an authorization code.',
  exchange_failed: 'Failed to exchange the authorization code with Google.',
  no_refresh_token: 'Google did not return a refresh token. Revoke access at https://myaccount.google.com/permissions and try again.',
  userinfo_failed: 'Could not fetch your Google account info.',
  pending_expired: 'The OAuth flow timed out. Please start again.',
  state_mismatch: 'OAuth state mismatch (pending). Please start again.',
  encrypt_failed: 'Internal error encrypting the refresh token.',
  create_failed: 'Failed to create the source after successful OAuth.',
  start_via_prepare: 'Start the Google OAuth flow by submitting the form first.',
  google_account_mismatch: 'You signed in with a different Google account than the one this source syncs. Reconnect using the same Google account.',
  reconnect_failed: 'Failed to save the new Google authorization. Please try again.',
  reconnect_source_not_found: 'The source being reconnected no longer exists.',
};
