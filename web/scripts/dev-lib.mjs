import { spawn as nodeSpawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const HEALTH_URL = 'http://127.0.0.1:8081/healthz'
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds))

export function resolveDevelopmentPaths(scriptUrl = import.meta.url) {
  const scriptDirectory = path.dirname(fileURLToPath(scriptUrl))
  const webDirectory = path.dirname(scriptDirectory)
  const repository = path.dirname(webDirectory)
  return { repository, webDirectory, composeFile: path.join(repository, 'compose.yaml'), envFile: path.join(repository, '.env') }
}

export async function runCommand(spawn, command, args, options = {}) {
  return await new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: 'inherit', ...options })
    child.once('error', reject)
    child.once('exit', (code) => code === 0 ? resolve() : reject(new Error(`${command} exited with code ${code ?? 'unknown'}`)))
  })
}

export async function captureCommand(spawn, command, args, options = {}) {
  return await new Promise((resolve, reject) => {
    const child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'], ...options })
    let stdout = ''; let stderr = ''
    child.stdout?.on('data', (chunk) => { stdout += chunk })
    child.stderr?.on('data', (chunk) => { stderr += chunk })
    child.once('error', reject)
    child.once('exit', (code) => code === 0 ? resolve({ stdout, stderr }) : reject(new Error(`${command} exited with code ${code ?? 'unknown'}: ${stderr.trim() || stdout.trim()}`)))
  })
}

function composeArgs(paths, ...args) { return ['compose', '--file', paths.composeFile, '--env-file', paths.envFile, ...args] }

export async function inspectControl(spawn, paths) {
  const { stdout } = await captureCommand(spawn, 'docker', composeArgs(paths, 'ps', '--all', '--format', 'json', 'tradeedge-control'), { cwd: paths.repository })
  const records = stdout.trim().split(/\r?\n/).filter(Boolean).flatMap((line) => {
    try { const value = JSON.parse(line); return Array.isArray(value) ? value : [value] } catch { return [] }
  })
  const record = records[0]
  if (!record) return { state: 'missing', health: 'missing', exitCode: null }
  return { state: String(record.State ?? record.Status ?? 'unknown').toLowerCase(), health: String(record.Health || 'none').toLowerCase(), exitCode: record.ExitCode ?? null }
}

async function probeControl(fetcher) {
  try {
    const response = await fetcher(HEALTH_URL, { signal: AbortSignal.timeout(2_000) })
    return response.ok ? { reachable: true, reason: 'healthy' } : { reachable: false, reason: `HTTP_${response.status}` }
  } catch (error) {
    const code = error?.cause?.code ?? error?.code
    const reason = code === 'ECONNREFUSED' || /refused/i.test(error?.message ?? '') ? 'connection_refused' : (error instanceof Error ? error.message : 'unreachable')
    return { reachable: false, reason }
  }
}

export async function waitForControl(fetcher, inspect, { attempts = 40, interval = 500, sleep = delay } = {}) {
  let lastProbe = { reason: 'not_probed' }; let lastContainer = { state: 'unknown', health: 'unknown', exitCode: null }
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    lastProbe = await probeControl(fetcher)
    if (lastProbe.reachable) return
    lastContainer = await inspect()
    if (lastContainer.state === 'exited' || lastContainer.state === 'dead') {
      const error = new Error(`container exited${lastContainer.exitCode == null ? '' : ` with code ${lastContainer.exitCode}`}`)
      error.diagnostics = { container: lastContainer, health: lastContainer.health, reason: error.message }
      throw error
    }
    if (attempt + 1 < attempts) await sleep(interval)
  }
  const health = lastContainer.health === 'unhealthy' ? 'unhealthy' : 'timeout'
  const error = new Error(`control-plane health timed out after ${attempts} attempts: ${lastProbe.reason}`)
  error.diagnostics = { container: lastContainer, health, reason: error.message }
  throw error
}

async function reportFailure(spawn, paths, error, log) {
  let container = error?.diagnostics?.container
  if (!container) { try { container = await inspectControl(spawn, paths) } catch { container = { state: 'unknown', health: 'unknown' } } }
  log.error('CONTROL_PLANE_START=FAIL')
  log.error(`CONTAINER_STATE=${container.state ?? 'unknown'}`)
  log.error(`HEALTH=${error?.diagnostics?.health ?? container.health ?? 'unknown'}`)
  log.error(`REASON=${error instanceof Error ? error.message : String(error)}`)
  try { await runCommand(spawn, 'docker', composeArgs(paths, 'logs', '--tail', '40', 'tradeedge-control'), { cwd: paths.repository }) } catch { /* best effort */ }
}

export async function ensureControl({ spawn, fetcher, paths, log, waitOptions } = {}) {
	await captureCommand(spawn, 'docker', composeArgs(paths, 'up', '-d', '--build', 'tradeedge-control'), { cwd: paths.repository })
	await waitForControl(fetcher, () => inspectControl(spawn, paths), waitOptions)
}

export async function launch({ spawn = nodeSpawn, fetcher = fetch, node = process.execPath, paths = resolveDevelopmentPaths(), log = console, waitOptions } = {}) {
  try { await runCommand(spawn, 'docker', ['version', '--format', '{{.Server.Version}}'], { cwd: paths.repository }) }
  catch { throw new Error('Docker is unavailable. Start Docker Desktop, then run npm run dev again.') }
  try { await ensureControl({ spawn, fetcher, paths, log, waitOptions }) }
  catch (error) { await reportFailure(spawn, paths, error, log); throw error }
  log.info('TradeEdge control plane is ready. Starting operator console…')
  const vite = spawn(node, [path.join(paths.webDirectory, 'node_modules', 'vite', 'bin', 'vite.js')], { cwd: paths.webDirectory, stdio: 'inherit' })
  const stop = () => { if (!vite.killed) vite.kill('SIGINT') }
  process.once('SIGINT', stop); process.once('SIGTERM', stop)
  return await new Promise((resolve, reject) => {
    vite.once('error', reject)
    vite.once('exit', (code, signal) => { process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop); if (code === 0 || signal === 'SIGINT') resolve(); else reject(new Error(`Vite exited with code ${code ?? 'unknown'}`)) })
  })
}
