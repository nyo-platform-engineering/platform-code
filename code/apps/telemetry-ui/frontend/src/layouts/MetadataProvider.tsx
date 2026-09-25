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
    permissions: string[]
  }
  capabilities: Capability[]
}

type MetadataState = {
  metadata: Metadata | null
  problem: string | null
}

const MetadataContext = createContext<MetadataState>({ metadata: null, problem: null })

export function useMetadata() {
  return useContext(MetadataContext)
}

export function MetadataProvider({ children }: { children: ReactNode }) {
  const [metadata, setMetadata] = useState<Metadata | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/meta', { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Metadata unavailable (${response.status})`)
        return response.json() as Promise<Metadata>
      })
      .then(setMetadata)
      .catch((error: Error) => {
        if (!controller.signal.aborted) setProblem(error.message)
      })
    return () => controller.abort()
  }, [])
  return (
    <MetadataContext.Provider value={{ metadata, problem }}>{children}</MetadataContext.Provider>
  )
}
