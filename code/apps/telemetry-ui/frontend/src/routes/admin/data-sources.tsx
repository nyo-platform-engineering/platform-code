import { createFileRoute } from '@tanstack/react-router'
import { DataSourcesPage } from '../../pages/admin/DataSourcesPage'

export const Route = createFileRoute('/admin/data-sources')({ component: DataSourcesPage })
