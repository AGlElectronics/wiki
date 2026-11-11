import type { User } from 'oidc-client-ts';

import { setUnauthorizedHandler } from '@/api/client';
import { getUserManager } from '@/auth/oidc';

/*
 * Keeps this tab on the identity Keycloak currently has. Tokens live only in
 * this tab's memory (token contract), so tabs share nothing but the
 * Keycloak session, which the session monitor watches:
 *  - the Keycloak session ends (signed out in any tab or app) -> login page
 *  - someone else signs in (session changed)  -> silent sign-in; reload if
 *    the person changed
 *  - the API answers 401                       -> login page
 */

export function goToLogin(): void {
  const path = `${window.location.pathname}${window.location.search}`;
  if (path.startsWith('/login') || path.startsWith('/auth/')) return;
  window.location.assign(`/login?redirect=${encodeURIComponent(path)}`);
}

let installed = false;

export function installSessionSync(): void {
  const mgr = getUserManager();
  if (!mgr || installed || typeof window === 'undefined') return;
  installed = true;

  let currentSub: string | undefined;
  void mgr.getUser().then((u) => {
    currentSub = u?.profile.sub;
  });
  mgr.events.addUserLoaded((u: User) => {
    if (currentSub !== undefined && u.profile.sub !== currentSub) {
      window.location.reload();
      return;
    }
    currentSub = u.profile.sub;
  });

  const endSession = () => {
    void mgr.removeUser().finally(goToLogin);
  };
  mgr.events.addUserSignedOut(endSession);
  // New Keycloak session (signed out and in elsewhere, maybe as someone
  // else): adopt it silently; addUserLoaded reloads if the person changed.
  mgr.events.addUserSessionChanged(() => {
    void mgr.signinSilent().catch(endSession);
  });
  mgr.events.addSilentRenewError((err) => {
    if (/login_required|invalid_grant|session not active/i.test(String(err?.message ?? err))) endSession();
  });

  let redirecting = false;
  setUnauthorizedHandler(() => {
    if (redirecting) return;
    redirecting = true;
    endSession();
  });
}
