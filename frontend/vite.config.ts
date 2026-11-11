import { readFileSync, existsSync } from 'node:fs'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig(({ command, isPreview }) => {
  const target = process.env.API_TARGET ?? 'http://127.0.0.1:8080'
  const proxy = Object.fromEntries(['/api/v1', '/csrf', '/openapi.json'].map(path => [path, { target }]))
  const key = '.tls/localhost.key'
  const cert = '.tls/localhost.crt'
  if (command === 'serve' && !isPreview && (!existsSync(key) || !existsSync(cert))) {
    throw new Error('Local HTTPS requires .tls/localhost.key and .tls/localhost.crt (see DEV.md).')
  }
  return { plugins: [vue()], server: { host: 'localhost', port: 5173, strictPort: true, proxy,
    https: command === 'serve' && !isPreview ? { key: readFileSync(key), cert: readFileSync(cert) } : undefined } }
})
