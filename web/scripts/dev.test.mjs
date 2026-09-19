import { describe, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { launch, parseDotenv, startControl, waitForControl } from './dev-lib.mjs'

function successfulChild({ detached = false } = {}) { const child = new EventEmitter(); child.killed = false; child.kill = vi.fn(() => { child.killed = true }); child.unref = vi.fn(); if (!detached) queueMicrotask(() => child.emit('exit', 0, null)); return child }

describe('development launcher', () => {
  it('starts/reuses only the control plane before Vite', async () => {
    const calls = []; const spawn = vi.fn((command, args) => { calls.push([command,...args]); return successfulChild() })
    await launch({ spawn, fetcher: vi.fn(async () => ({ ok: true })), node: '/node', webDirectory: '/repo/web', log: { info: vi.fn() } })
    expect(calls[1]).toEqual(['docker','compose','--env-file','.env','stop','tradeedge-console','tradeedge-control'])
    expect(calls.some((call) => call[0] === 'go')).toBe(false)
    expect(calls.flat().join(' ')).not.toContain('tradeedge-shadow')
  })
  it('is safe to invoke repeatedly', async () => {
    const calls = []; const spawn = vi.fn((command, args) => { calls.push([command,...args]); return successfulChild() }); const options = { spawn, fetcher: vi.fn(async () => ({ ok: true })), node: '/node', webDirectory: '/repo/web', log: { info: vi.fn() } }
    await launch(options); await launch(options)
    expect(calls.filter((call) => call.includes('tradeedge-console'))).toHaveLength(2)
    expect(calls.flat().join(' ')).not.toContain('down')
  })
  it('parses quoted and unquoted environment values', () => {
    expect(parseDotenv("A=one\nB='two words'\nC=\"three\"\n# ignored")).toEqual({ A: 'one', B: 'two words', C: 'three' })
  })
  it('builds and detaches a host control plane with the validation binary', async () => {
    const repository = await mkdtemp(path.join(os.tmpdir(), 'tradeedge-dev-'))
    await mkdir(path.join(repository, 'web'))
    await writeFile(path.join(repository, '.env'), 'ZERODHA_API_KEY=test-key\n')
    const calls = []
    const spawn = vi.fn((command, args, options) => {
      calls.push({ command, args, options })
      return successfulChild({ detached: options.detached })
    })
    try {
      await startControl(spawn, repository)
      expect(calls.filter(({ command }) => command === 'go')).toHaveLength(2)
      const control = calls.at(-1)
      expect(control.options.detached).toBe(true)
      expect(control.options.env.ZERODHA_API_KEY).toBe('test-key')
      expect(control.options.env.TRADEEDGE_VALIDATION_COMMAND).toContain('tradeedge-validation')
    } finally {
      await rm(repository, { recursive: true, force: true })
    }
  })
  it('fails with an actionable message when Docker is unavailable', async () => {
    const spawn = vi.fn(() => { const child = new EventEmitter(); queueMicrotask(() => child.emit('error', new Error('ENOENT'))); return child })
    await expect(launch({ spawn, webDirectory: '/repo/web' })).rejects.toThrow('Start Docker Desktop')
  })
  it('waits until control becomes healthy', async () => {
    const fetcher = vi.fn().mockRejectedValueOnce(new Error('refused')).mockResolvedValue({ ok: true })
    await waitForControl(fetcher, { attempts: 2, interval: 0 }); expect(fetcher).toHaveBeenCalledTimes(2)
  })
  it('fails when control never becomes healthy', async () => {
    await expect(waitForControl(vi.fn(async () => { throw new Error('refused') }), { attempts: 2, interval: 0 })).rejects.toThrow('did not become healthy')
  })
})
