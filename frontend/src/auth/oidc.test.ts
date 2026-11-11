// Token contract: tokens stay in memory (never localStorage or
// sessionStorage), the API gets the access token, and a reload signs in
// again silently.
import { User } from 'oidc-client-ts';
import { beforeEach, vi } from 'vitest';

vi.mock('@/runtime-config', () => ({
  runtimeConfig: () => ({
    authMode: 'oidc',
    oidcAuthority: 'https://auth.example.org/realms/conservices',
    oidcClientId: 'wiki-spa',
    oidcScope: 'openid profile email',
    apiBaseUrl: '',
    productName: 'Wiki',
  }),
}));

const resolvers: (() => Promise<Record<string, string>>)[] = [];
vi.mock('@/api/client', () => ({
  setAuthHeadersResolver: (fn: () => Promise<Record<string, string>>) => resolvers.push(fn),
  setUnauthorizedHandler: () => undefined,
}));

const { ensureUser, getUserManager } = await import('@/auth/oidc');
const { installAuth } = await import('@/auth/installAuth');

function user(overrides: Partial<ConstructorParameters<typeof User>[0]> = {}) {
  return new User({
    id_token: 'the-id-token',
    access_token: 'the-access-token',
    token_type: 'Bearer',
    profile: { sub: 'kc-1', iss: 'https://auth.example.org/realms/conservices', aud: 'wiki-spa', exp: 0, iat: 0 },
    expires_at: Math.floor(Date.now() / 1000) + 300,
    ...overrides,
  });
}

beforeEach(async () => {
  await getUserManager()!.removeUser();
  localStorage.clear();
  sessionStorage.clear();
  vi.restoreAllMocks();
});

test('the signed-in user is kept in memory, not in web storage', async () => {
  const mgr = getUserManager()!;
  await mgr.storeUser(user());
  expect((await mgr.getUser())?.access_token).toBe('the-access-token');
  const stored = [...Object.keys(localStorage), ...Object.keys(sessionStorage)];
  expect(stored.filter((k) => k.startsWith('oidc.user'))).toEqual([]);
  expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain('the-access-token');
});

test('the API gets the access token, never the ID token', async () => {
  installAuth();
  await getUserManager()!.storeUser(user());
  const headers = await resolvers.at(-1)!();
  expect(headers).toEqual({ Authorization: 'Bearer the-access-token' });
});

test('without a user in memory (after a reload) one silent sign-in runs', async () => {
  const mgr = getUserManager()!;
  const silent = vi.spyOn(mgr, 'signinSilent').mockResolvedValue(user({ access_token: 'renewed' }));
  const [a, b] = await Promise.all([ensureUser(), ensureUser()]);
  expect(a?.access_token).toBe('renewed');
  expect(b?.access_token).toBe('renewed');
  expect(silent).toHaveBeenCalledTimes(1);
});

test('no Keycloak session: no user, no header', async () => {
  installAuth();
  vi.spyOn(getUserManager()!, 'signinSilent').mockRejectedValue(new Error('login_required'));
  expect(await ensureUser()).toBeNull();
  expect(await resolvers.at(-1)!()).toEqual({});
});
