import { createWebHistory, createRouter } from 'vue-router'
import { createApp } from 'vue'
import App from './App.vue'
import i18n from './i18n/index'
import { createAuthGuard } from './security/auth'

import RegisterView from './views/RegisterView.vue'
import LoginView from './views/LoginView.vue'
import AdminUser from './views/AdminUser.vue'
import AdminSetting from './views/AdminSetting.vue'
import AdminLicense from './views/AdminLicense.vue'
import LoadingView from './views/LoadingView.vue'
import RouteView from './views/RouteView.vue'
import UserView from './views/UserView.vue'
import DeviceView from './views/DeviceView.vue'
import NetworkView from './views/NetworkView.vue'
import StatisticsView from './views/StatisticsView.vue'

const routes = [
  { path: '/', component: LoadingView },
  { path: '/login', component: LoginView, meta: { public: true } },
  { path: '/register', component: RegisterView, meta: { public: true } },
  { path: '/statistics', component: StatisticsView, meta: { role: 'normal' } },
  { path: '/network', component: NetworkView, meta: { role: 'normal' } },
  { path: '/device', component: DeviceView, meta: { role: 'normal' } },
  { path: '/route', component: RouteView, meta: { role: 'normal' } },
  { path: '/user', component: UserView, meta: { role: 'normal' } },
  { path: '/admin/license', component: AdminLicense, meta: { role: 'admin' } },
  { path: '/admin/user', component: AdminUser, meta: { role: 'admin' } },
  { path: '/admin/setting', component: AdminSetting, meta: { role: 'admin' } },
  { path: '/:pathMatch(.*)', redirect: '/' }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach(createAuthGuard())

const app = createApp(App)
app.use(router)
app.use(i18n)
app.mount('#app')
