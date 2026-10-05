package caldav

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/oauth2"

	"github.com/macjediwizard/calbridgesync/internal/db"
)

// IsNotFoundError reports whether err means the remote object does not
// exist (HTTP 404). go-webdav's HTTPError type is internal, so the
// status is matched on its "404 Not Found" error text.
func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "404 Not Found")
}

// DeleteSourceEvent deletes one object from a source calendar, building
// the source client the same way SyncSource does (OAuth2 for Google,
// Basic Auth otherwise). A 404 counts as success: the object is gone.
// Any other failure, including credential decryption, is returned so the
// caller can keep its tracking state. (#202)
func (se *SyncEngine) DeleteSourceEvent(ctx context.Context, source *db.Source, eventPath string) error {
	if eventPath == "" {
		return errors.New("event path is required")
	}
	if source.SourceType == db.SourceTypeICS {
		return errors.New("ICS sources are read-only")
	}

	var client *Client
	if source.SourceType == db.SourceTypeGoogle {
		if source.OAuthRefreshToken == "" {
			return errors.New("google source is missing its OAuth refresh token")
		}
		oauthConfig, err := se.buildPerSourceGoogleOAuthConfig(source, "")
		if err != nil {
			return err
		}
		refreshToken, err := se.encryptor.Decrypt(source.OAuthRefreshToken)
		if err != nil {
			return fmt.Errorf("failed to decrypt Google OAuth refresh token: %w", err)
		}
		client, err = NewOAuthClient(ctx, source.SourceURL, oauthConfig, &oauth2.Token{RefreshToken: refreshToken})
		if err != nil {
			return err
		}
	} else {
		password, err := se.encryptor.Decrypt(source.SourcePassword)
		if err != nil {
			return fmt.Errorf("failed to decrypt source credentials: %w", err)
		}
		client, err = NewClient(source.SourceURL, source.SourceUsername, password)
		if err != nil {
			return err
		}
	}

	if err := client.DeleteEvent(ctx, eventPath); err != nil && !IsNotFoundError(err) {
		return err
	}
	return nil
}
