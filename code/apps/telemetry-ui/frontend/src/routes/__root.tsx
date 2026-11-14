import { createRootRoute } from '@tanstack/react-router'
import AppLayout from '../layouts/AppLayout'
import { NotFoundPage } from '../pages/not-found'
export const Route = createRootRoute({ component: AppLayout, notFoundComponent: NotFoundPage })
