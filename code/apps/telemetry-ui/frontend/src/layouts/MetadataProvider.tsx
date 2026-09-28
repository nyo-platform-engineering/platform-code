import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
export type Capability = {
  id: string
  title: string
  status: 'planned' | 'available'
  bucket: string
  description: string
}

type Metadata = {
  service: string
  version: string
  actor: {
    displayName: string
    tenant: string
    organizationId: string
    organizationName: string
    organizationScope: string
    permissions: string[]
  }
  capabilities: Capability[]
}

export type DataSourceOption = {
  id: string
  address: string
  database: string
  secure: boolean
  managedBy: string
}

type DataSources = { traces: DataSourceOption[]; logs: DataSourceOption[] }

type MetadataState = {
  metadata: Metadata | null
  dataSources: DataSources
  problem: string | null
}

const MetadataContext = createContext<MetadataState>({
  metadata: null,
  dataSources: { traces: [], logs: [] },
  problem: null,
})

export function useMetadata() {
  return useContext(MetadataContext)
}

export function MetadataProvider({ children }: { children: ReactNode }) {
  const [metadata, setMetadata] = useState<Metadata | null>(null)
  const [dataSources, setDataSources] = useState<DataSources>({ traces: [], logs: [] })
  const [problem, setProblem] = useState<string | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    const load = async <T,>(path: string) => {
      const response = await fetch(path, { signal: controller.signal })
      if (response.status === 401) window.dispatchEvent(new Event('session-expired'))
      if (!response.ok) throw new Error(`Metadata unavailable (${response.status})`)
      return response.json() as Promise<T>
    }
    Promise.all([load<Metadata>('/api/v1/meta'), load<DataSources>('/api/v1/data-sources')])
      .then(([nextMetadata, nextDataSources]) => {
        setMetadata(nextMetadata)
        setDataSources(nextDataSources)
      })
      .catch((error: Error) => {
        if (!controller.signal.aborted) setProblem(error.message)
      })
    return () => controller.abort()
  }, [])
  return (
    <MetadataContext.Provider value={{ metadata, dataSources, problem }}>
      {children}
    </MetadataContext.Provider>
  )
}
