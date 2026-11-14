import { createFileRoute } from '@tanstack/react-router'
import { OrganizationsPage } from '../../pages/admin/OrganizationsPage'

export const Route = createFileRoute('/admin/organizations')({ component: OrganizationsPage })
