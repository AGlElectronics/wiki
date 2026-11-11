import {
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  redirect,
} from '@tanstack/react-router';

import { Layout } from '@/components/Layout';
import { HomePage } from '@/pages/HomePage';
import { LoginPage } from '@/pages/LoginPage';
import { OidcCallbackPage } from '@/pages/OidcCallbackPage';
import { PageView } from '@/pages/PageView';
import { SettingsPage } from '@/pages/SettingsPage';
import { PublicSharePage } from '@/pages/SharePage';
import { SpacePage } from '@/pages/SpacePage';
import { TrashPage } from '@/pages/TrashPage';
import { ensureUser, isOidcAuthMode } from '@/auth/oidc';

// In OIDC mode a route behind the Layout checks there is a user (in memory,
// or by a silent sign-in after a reload) before letting the component
// mount. If not, we redirect to /login and carry the attempted path so the
// post-login redirect lands there.
async function requireAuth(fullPath: string) {
  if (!isOidcAuthMode()) {
    return; // dev mode: nothing to check
  }
  const user = await ensureUser();
  if (!user || user.expired) {
    throw redirect({ to: '/login', search: { redirect: fullPath } });
  }
}

const rootRoute = createRootRoute({
  component: () => <Outlet />,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>) => ({
    redirect: typeof search.redirect === 'string' ? search.redirect : undefined,
  }),
});

const authCallbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/callback',
  component: OidcCallbackPage,
});

// Public share links need no account: the token in the URL is the access.
const shareRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/share/$token',
  component: PublicSharePage,
});

const appLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  component: Layout,
});

const indexRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: '/',
  beforeLoad: ({ location }) => requireAuth(location.pathname),
  component: HomePage,
});

const spaceRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: '/s/$spaceId',
  beforeLoad: ({ location }) => requireAuth(location.pathname),
  component: SpacePage,
});

const trashRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: '/s/$spaceId/trash',
  beforeLoad: ({ location }) => requireAuth(location.pathname),
  component: TrashPage,
});

const pageRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: '/p/$pageId',
  beforeLoad: ({ location }) => requireAuth(location.pathname),
  component: PageView,
});

const settingsRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: '/settings',
  beforeLoad: ({ location }) => requireAuth(location.pathname),
  component: SettingsPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  authCallbackRoute,
  shareRoute,
  appLayoutRoute.addChildren([
    indexRoute,
    spaceRoute,
    trashRoute,
    pageRoute,
    settingsRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
