import { spawn as nodeSpawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { mkdir, readFile } from 'node:fs/promises'

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

export async function runCommand(spawn, command, args, options = {}) {
  return await new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: 'inherit', ...options })
    child.once('error', reject)
    child.once('exit', (code) => code === 0 ? resolve() : reject(new Error(`${command} exited with code ${code ?? 'unknown'}`)))
  })
}

export async function waitForControl(fetcher, { attempts = 40, interval = 500 } = {}) {
  let lastError
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const response = await fetcher('http://127.0.0.1:8081/healthz', { signal: AbortSignal.timeout(2_000) })
      if (response.ok) return
      lastError = new Error(`control plane returned HTTP ${response.status}`)
    } catch (error) { lastError = error }
    await delay(interval)
  }
  throw new Error(`control plane did not become healthy: ${lastError instanceof Error ? lastError.message : 'unreachable'}`)
}

async function controlAvailable(fetcher) {
  try {
    const response = await fetcher('http://127.0.0.1:8081/healthz', { signal: AbortSignal.timeout(1_000) })
    return response.ok
  } catch {
    return false
  }
}

export function parseDotenv(contents) {
  const result = {}
  for (const raw of contents.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const split = line.indexOf('=')
    if (split < 1) continue
    let value = line.slice(split + 1).trim()
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) value = value.slice(1, -1)
    result[line.slice(0, split).trim()] = value
  }
  return result
}

export async function startControl(spawn, repository) {
  const binaryDirectory = path.join(repository, '.cache', 'dev-bin')
  const executableSuffix = process.platform === 'win32' ? '.exe' : ''
  const validationBinary = path.join(binaryDirectory, `tradeedge-validation${executableSuffix}`)
  const controlBinary = path.join(binaryDirectory, `tradeedge-control${executableSuffix}`)
  await mkdir(binaryDirectory, { recursive: true })
  await runCommand(spawn, 'go', ['build', '-o', validationBinary, './cmd/tradeedge-validation'], { cwd: repository })
  await runCommand(spawn, 'go', ['build', '-o', controlBinary, './cmd/tradeedge-control'], { cwd: repository })

  const environment = {
    ...process.env,
    ...parseDotenv(await readFile(path.join(repository, '.env'), 'utf8')),
    TRADEEDGE_REPOSITORY: repository,
    TRADEEDGE_VALIDATION_COMMAND: validationBinary,
    TRADEEDGE_SHADOW_READINESS_URL: 'http://127.0.0.1:8080/readyz',
  }
  const control = spawn(controlBinary, [], { cwd: repository, env: environment, detached: true, stdio: 'ignore' })
  control.once('error', () => {})
  control.unref()
}

export async function launch({ spawn = nodeSpawn, fetcher = fetch, node = process.execPath, webDirectory, log = console } = {}) {
  const resolvedWeb = webDirectory ?? path.dirname(path.dirname(fileURLToPath(import.meta.url)))
  const repository = path.dirname(resolvedWeb)
  try {
    await runCommand(spawn, 'docker', ['version', '--format', '{{.Server.Version}}'], { cwd: repository })
  } catch {
    throw new Error('Docker is unavailable. Start Docker Desktop, then run npm run dev again.')
  }
  try {
    await runCommand(spawn, 'docker', ['compose', '--env-file', '.env', 'stop', 'tradeedge-console', 'tradeedge-control'], { cwd: repository })
    if (!await controlAvailable(fetcher)) await startControl(spawn, repository)
    await waitForControl(fetcher)
  } catch (error) {
    try { await runCommand(spawn, 'docker', ['compose', '--env-file', '.env', 'logs', '--tail', '40', 'tradeedge-control'], { cwd: repository }) } catch { /* diagnostics are best effort */ }
    throw error
  }
  log.info('TradeEdge control plane is ready. Starting operator console…')
  const viteEntry = path.join(resolvedWeb, 'node_modules', 'vite', 'bin', 'vite.js')
  const vite = spawn(node, [viteEntry], { cwd: resolvedWeb, stdio: 'inherit' })
  const stop = () => { if (!vite.killed) vite.kill('SIGINT') }
  process.once('SIGINT', stop); process.once('SIGTERM', stop)
  return await new Promise((resolve, reject) => {
    vite.once('error', reject)
    vite.once('exit', (code, signal) => { process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop); if (code === 0 || signal === 'SIGINT') resolve(); else reject(new Error(`Vite exited with code ${code ?? 'unknown'}`)) })
  })
}
