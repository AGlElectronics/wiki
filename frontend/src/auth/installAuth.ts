import { setAuthHeadersResolver } from '@/api/client';
import { installDevAuth } from '@/auth/dev';
import { ensureUser, isOidcAuthMode } from '@/auth/oidc';
import { installSessionSync } from '@/auth/session';

/** Wire the API client's auth header pipeline. Call once at startup. */
export function installAuth(): void {
  if (isOidcAuthMode()) {
    setAuthHeadersResolver(async (): Promise<Record<string, string>> => {
      // Token contract: the access token (audience wiki-api), never the
      // ID token; the backend refuses ID tokens.
      const user = await ensureUser();
      if (!user?.access_token || user.expired) {
        return {};
      }
      return { Authorization: `Bearer ${user.access_token}` };
    });
    installSessionSync();
    return;
  }
  installDevAuth();
}
