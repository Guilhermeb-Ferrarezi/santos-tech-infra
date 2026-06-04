import React from 'react'
import ReactDOM from 'react-dom/client'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import LoginPage from '@/routes/index'
import ForgotPasswordPage from '@/routes/forgot-password'
import ResetPasswordPage from '@/routes/reset-password'
import ConfirmAccessPage from '@/routes/confirm'
import '@/styles.css'

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

const rootRoute = createRootRoute({ component: () => <Outlet /> })
const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: LoginPage })
const forgotRoute = createRoute({ getParentRoute: () => rootRoute, path: '/forgot-password', component: ForgotPasswordPage })
const resetRoute = createRoute({ getParentRoute: () => rootRoute, path: '/reset-password', component: ResetPasswordPage })
const confirmRoute = createRoute({ getParentRoute: () => rootRoute, path: '/confirm', component: ConfirmAccessPage })

const router = createRouter({
  routeTree: rootRoute.addChildren([loginRoute, forgotRoute, resetRoute, confirmRoute]),
})

declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>
)
