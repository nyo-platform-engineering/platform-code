import { createRootRoute } from '@tanstack/react-router'
import App, { NotFoundPage } from '../App'

export const Route = createRootRoute({
  component: App,
  notFoundComponent: NotFoundPage,
})
