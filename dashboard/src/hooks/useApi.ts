// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { useState, useEffect, useCallback } from 'react'

export function useApi<T>(
  fetcher: () => Promise<T>,
  intervalMs?: number
) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const result = await fetcher()
      setData(result)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [fetcher])

  useEffect(() => {
    refresh()
    if (intervalMs && intervalMs > 0) {
      const id = setInterval(refresh, intervalMs)
      return () => clearInterval(id)
    }
  }, [refresh, intervalMs])

  return { data, error, loading, refresh }
}
