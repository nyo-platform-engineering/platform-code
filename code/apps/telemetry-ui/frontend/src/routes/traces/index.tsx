import { createFileRoute } from '@tanstack/react-router'
import { TracesPage } from '../../pages/traces'

export const Route = createFileRoute('/traces/')({ component: TracesPage })
