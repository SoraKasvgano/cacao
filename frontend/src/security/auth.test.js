import assert from 'node:assert/strict'
import test from 'node:test'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createAuthGuard } from './auth.js'

const createRouterFor = (post) => {
  const component = { render: () => null }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component },
      { path: '/login', component, meta: { public: true } },
      { path: '/register', component, meta: { public: true } },
      { path: '/statistics', component, meta: { role: 'normal' } },
      { path: '/network', component, meta: { role: 'normal' } },
      { path: '/admin/setting', component, meta: { role: 'admin' } }
    ]
  })
  router.beforeEach(createAuthGuard({ post }))
  return router
}

const session = (role) => ({ data: { status: 0, data: { role } } })

test('public login and registration do not require a session', async () => {
  const router = createRouterFor(() => assert.fail('must not query the session'))
  await router.push('/login')
  assert.equal(router.currentRoute.value.path, '/login')
  await router.push('/register')
  assert.equal(router.currentRoute.value.path, '/register')
})

test('anonymous users cannot navigate directly to protected pages', async () => {
  const router = createRouterFor(async () => ({ data: { status: 2 } }))
  for (const path of ['/admin/setting', '/network', '/']) {
    await router.push(path)
    assert.equal(router.currentRoute.value.path, '/login')
  }
})

test('ordinary users are redirected away from administrator pages', async () => {
  const router = createRouterFor(async () => session('normal'))
  await router.push('/admin/setting')
  assert.equal(router.currentRoute.value.path, '/statistics')
  await router.push('/network')
  assert.equal(router.currentRoute.value.path, '/network')
})

test('administrators retain their management landing page', async () => {
  const router = createRouterFor(async () => session('admin'))
  await router.push('/')
  assert.equal(router.currentRoute.value.path, '/admin/setting')
  await router.push('/network')
  assert.equal(router.currentRoute.value.path, '/admin/setting')
})

test('an expired session is checked again on the next navigation', async () => {
  let current = session('normal')
  const router = createRouterFor(async () => current)
  await router.push('/statistics')
  current = { data: { status: 2 } }
  await router.push('/network')
  assert.equal(router.currentRoute.value.path, '/login')
})

test('failed requests and unrecognized roles cannot authorize a page', async () => {
  const responses = [
    async () => { throw new Error('unavailable') },
    async () => session('unknown'),
    async () => ({ data: { status: 11, data: { role: 'admin' } } }),
    async () => ({ data: {} })
  ]
  for (const post of responses) {
    const router = createRouterFor(post)
    await router.push('/admin/setting')
    assert.equal(router.currentRoute.value.path, '/login')
  }
})
