import axios from 'axios'

// The API remains the authorization boundary. Check the current session before
// mounting a protected page, without trusting a role cached in browser storage.
export const createAuthGuard = (client = axios) => async (to) => {
  if (to.meta.public) return true

  try {
    const response = await client.post('/api/user/info', null, {
      skipAuthRedirect: true,
      timeout: 10000
    })
    const role = response.data?.status === 0 ? response.data.data?.role : null
    if (role !== 'admin' && role !== 'normal') return '/login'

    const home = role === 'admin' ? '/admin/setting' : '/statistics'
    if (to.path === '/' || (to.meta.role && to.meta.role !== role)) return home
    return true
  } catch {
    return '/login'
  }
}
