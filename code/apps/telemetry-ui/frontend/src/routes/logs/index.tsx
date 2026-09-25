import { createFileRoute } from '@tanstack/react-router'
import { LogsPage } from '../../pages/logs'

export const Route = createFileRoute('/logs/')({ component: LogsPage })
