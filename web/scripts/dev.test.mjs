import { describe, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'
import { fileURLToPath, pathToFileURL } from 'node:url'
import path from 'node:path'
import { ensureControl, launch, resolveDevelopmentPaths, waitForControl } from './dev-lib.mjs'

const paths = { repository: '/repo', webDirectory: '/repo/web', composeFile: '/repo/compose.yaml', envFile: '/repo/.env' }
function child({ code = 0, stdout = '', stderr = '' } = {}) { const value = new EventEmitter(); value.stdout = new EventEmitter(); value.stderr = new EventEmitter(); value.killed = false; value.kill = vi.fn(() => { value.killed = true }); queueMicrotask(() => { if (stdout) value.stdout.emit('data', stdout); if (stderr) value.stderr.emit('data', stderr); value.emit('exit', code, null) }); return value }
function spawnForStates(states, calls = []) { return vi.fn((command, args) => { calls.push([command, ...args]); if (args?.includes('ps')) { const state = states.shift(); return child({ stdout: state ? `${JSON.stringify(state)}\n` : '' }) } return child() }) }
const quiet = () => ({ info: vi.fn(), error: vi.fn() })

describe('development launcher', () => {
  it('reuses a healthy control plane without stopping or recreating it', async () => {
    const calls = []; const spawn = spawnForStates([{ State: 'running', Health: 'healthy' }], calls)
    await launch({ spawn, fetcher: vi.fn(async () => ({ ok: true })), node: '/node', paths, log: quiet() })
    expect(calls.flat().join(' ')).not.toMatch(/\bstop\b|\bup\b/)
  })

  it.each([['stopped', { State: 'exited', Health: '', ExitCode: 0 }], ['missing', null]])('starts a %s control plane with compose up', async (_name, initial) => {
    const calls = []; const spawn = spawnForStates([initial], calls)
    const fetcher = vi.fn().mockRejectedValueOnce(Object.assign(new Error('connect ECONNREFUSED'), { code: 'ECONNREFUSED' })).mockResolvedValue({ ok: true })
    await ensureControl({ spawn, fetcher, paths, log: quiet(), waitOptions: { attempts: 2, interval: 0 } })
    expect(calls.some((call) => call.includes('up') && call.includes('-d') && call.includes('tradeedge-control'))).toBe(true)
  })

  it('performs bounded recovery for an unhealthy control plane', async () => {
    const calls = []; const spawn = spawnForStates([{ State: 'running', Health: 'unhealthy' }, { State: 'running', Health: 'starting' }], calls)
    const fetcher = vi.fn().mockResolvedValueOnce({ ok: false, status: 503 }).mockResolvedValueOnce({ ok: false, status: 503 }).mockResolvedValue({ ok: true })
    await ensureControl({ spawn, fetcher, paths, log: quiet(), waitOptions: { attempts: 2, interval: 0 } })
    expect(fetcher).toHaveBeenCalledTimes(3); expect(calls.some((call) => call.includes('up'))).toBe(true)
  })

  it('prints useful diagnostics and logs when compose startup fails', async () => {
    const log = quiet(); const calls = []; const spawn = vi.fn((command, args) => { calls.push([command, ...args]); if (args?.includes('ps')) return child({ stdout: `${JSON.stringify({ State: 'exited', Health: '', ExitCode: 1 })}\n` }); if (args?.includes('up')) return child({ code: 1, stderr: 'compose exploded' }); return child() })
    await expect(launch({ spawn, fetcher: vi.fn(async () => { throw new Error('refused') }), paths, log })).rejects.toThrow('compose exploded')
    expect(log.error.mock.calls.flat()).toEqual(expect.arrayContaining(['CONTROL_PLANE_START=FAIL', 'CONTAINER_STATE=exited', 'HEALTH=none'])); expect(calls.some((call) => call.includes('logs') && call.includes('40'))).toBe(true)
  })

  it('reports a bounded health timeout with state and reason', async () => {
    const log = quiet(); const spawn = spawnForStates([{ State: 'running', Health: 'starting' }, { State: 'running', Health: 'unhealthy' }, { State: 'running', Health: 'unhealthy' }])
    await expect(launch({ spawn, fetcher: vi.fn(async () => ({ ok: false, status: 503 })), paths, log, waitOptions: { attempts: 2, interval: 0 } })).rejects.toThrow('timed out after 2 attempts')
    expect(log.error.mock.calls.flat()).toEqual(expect.arrayContaining(['CONTAINER_STATE=running', 'HEALTH=unhealthy']))
  })

  it.each(['LOGIN_REQUIRED', 'MARKET_CLOSED'])('%s does not fail bootstrap when health is OK', async () => {
    await expect(launch({ spawn: spawnForStates([{ State: 'running', Health: 'healthy' }]), fetcher: vi.fn(async () => ({ ok: true })), node: '/node', paths, log: quiet() })).resolves.toBeUndefined()
  })

  it('resolves repository paths from the script URL, independent of cwd', () => {
    const script = pathToFileURL(path.join(path.sep, 'some', 'repo', 'web', 'scripts', 'dev-lib.mjs')).href; const resolved = resolveDevelopmentPaths(script)
    const expectedRepository = path.dirname(path.dirname(path.dirname(fileURLToPath(script))))
    expect(path.normalize(resolved.repository)).toBe(path.normalize(expectedRepository)); expect(resolved.composeFile).toBe(path.join(resolved.repository, 'compose.yaml'))
  })

  it('starts Vite only after the control plane is reachable', async () => {
    const sequence = []; const spawn = vi.fn((command, args) => { if (args?.includes('ps')) return child({ stdout: `${JSON.stringify({ State: 'exited', Health: '', ExitCode: 0 })}\n` }); if (command === '/node') sequence.push('vite'); return child() })
    const fetcher = vi.fn().mockImplementationOnce(async () => { sequence.push('initial-fail'); throw new Error('refused') }).mockImplementationOnce(async () => { sequence.push('health-ok'); return { ok: true } })
    await launch({ spawn, fetcher, node: '/node', paths, log: quiet(), waitOptions: { attempts: 1, interval: 0 } }); expect(sequence).toEqual(['initial-fail', 'health-ok', 'vite'])
  })

  it('distinguishes an exited container during polling', async () => {
    await expect(waitForControl(vi.fn(async () => { throw new Error('connection refused') }), async () => ({ state: 'exited', health: 'none', exitCode: 0 }), { attempts: 2, interval: 0 })).rejects.toThrow('container exited with code 0')
  })
})
