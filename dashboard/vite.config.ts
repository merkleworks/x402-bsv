// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../cmd/server/static',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8402',
      '/demo': 'http://localhost:8402',
      '/health': 'http://localhost:8402',
      '/v1': 'http://localhost:8402',
      '/nonce': 'http://localhost:8402',
      '/delegate': 'http://localhost:8402',
    },
  },
})
