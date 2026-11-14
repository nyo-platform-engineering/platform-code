import { createFileRoute } from '@tanstack/react-router'
import { AccessGrantsPage } from '../../pages/admin/AccessGrantsPage'

export const Route = createFileRoute('/admin/access-grants')({ component: AccessGrantsPage })
