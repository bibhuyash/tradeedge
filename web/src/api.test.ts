import { describe, expect, it } from 'vitest'
import { clearRequestToken, integrationDisplayState, runtimePollingEnabled, systemDisplayState } from './api'

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

describe('runtimePollingEnabled', () => {
  it('does not poll SHADOW while the market is closed', () => expect(runtimePollingEnabled('MARKET_CLOSED')).toBe(false))
  it('polls when the lifecycle reports a running runtime', () => { expect(runtimePollingEnabled('RUNNING')).toBe(true); expect(runtimePollingEnabled('READY')).toBe(true) })
  it('stops runtime polling after lifecycle degradation', () => expect(runtimePollingEnabled('FAILED')).toBe(false))
})

describe('expired session presentation', () => {
  it('keeps the system online while Zerodha requires login', () => { expect(systemDisplayState(false)).toBe('ONLINE'); expect(integrationDisplayState('EXPIRED', undefined, false)).toBe('OFFLINE') })
  it('does not enable SHADOW polling for an expired session lifecycle', () => expect(runtimePollingEnabled('LOGIN_REQUIRED')).toBe(false))
  it('clears the request token after submission', () => { const input = { value: 'sensitive-token' }; clearRequestToken(input); expect(input.value).toBe('') })
})
