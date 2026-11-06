package api

import (
	"context"

	"go.pilab.hu/agenthub/internal/authz"
)

// identCtx returns a context carrying a placeholder identity, exactly like the
// server's HeaderAuthenticator builds from the X-Auth-Subject/X-Auth-Scopes
// headers (M3.2).
func identCtx(subject string, scopes ...string) context.Context {
	return authz.WithIdentity(context.Background(), authz.Identity{
		Subject: subject,
		Scopes:  scopes,
	})
}
