import { describe, expect, it } from 'vitest'
import { integrationDisplayState } from './api'

describe('integrationDisplayState', () => {
  it('reports ONLINE only for an operational market stream', () => {
    expect(integrationDisplayState('AUTHENTICATED', { stream: { state: 'CONNECTED' } }, false)).toBe('ONLINE')
  })

  it('reports OFFLINE for an authenticated but disconnected stream', () => {
    expect(integrationDisplayState('AUTHENTICATED', { stream: { state: 'RECONNECTING' } }, false)).toBe('OFFLINE')
  })

  it('reports UNAVAILABLE when the status endpoint fails', () => {
    expect(integrationDisplayState('AUTHENTICATED', undefined, true)).toBe('UNAVAILABLE')
  })

  it('reports OFFLINE when Zerodha login is required', () => {
    expect(integrationDisplayState('LOGIN_REQUIRED', { stream: { state: 'CONNECTED' } }, false)).toBe('OFFLINE')
  })
})
