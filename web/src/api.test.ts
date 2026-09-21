import { describe, expect, it } from 'vitest'
import { clearRequestToken, continueAfterAuthentication, integrationDisplayState, runtimePollingEnabled, systemDisplayState, topLevelView } from './api'

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

describe('top-level authentication precedence', () => {
  it('renders login for LOGIN_REQUIRED on a weekend', () => expect(topLevelView(false, 'LOGIN_REQUIRED', 'MARKET_CLOSED')).toBe('LOGIN'))
  it('renders login for EXPIRED while the market is closed', () => expect(topLevelView(false, 'EXPIRED', 'MARKET_CLOSED')).toBe('LOGIN'))
  it('renders login for LOGIN_REQUIRED while the market is open', () => expect(topLevelView(false, 'LOGIN_REQUIRED', 'READY')).toBe('LOGIN'))
  it.each(['WEEKEND', 'HOLIDAY'])('renders market closed for an authenticated %s session', () => expect(topLevelView(false, 'AUTHENTICATED', 'MARKET_CLOSED')).toBe('MARKET_CLOSED'))
  it('renders the lifecycle dashboard for an authenticated open market', () => expect(topLevelView(false, 'AUTHENTICATED', 'READY')).toBe('RUNTIME'))
  it('gives control-plane failure priority over every other state', () => expect(topLevelView(true, 'LOGIN_REQUIRED', 'MARKET_CLOSED')).toBe('CONTROL_UNAVAILABLE'))
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

describe('post-authentication lifecycle', () => {
  it('clears the token, refreshes session state, and then continues startup', async () => {
    const order: string[] = []; const input = { value: 'sensitive-token' }
    await continueAfterAuthentication(input.value, input, { exchange: async (token) => { expect(token).toBe('sensitive-token'); order.push('exchange') }, refreshSession: async () => { expect(input.value).toBe(''); order.push('session') }, start: async () => { order.push('start') }, refreshStartup: async () => { order.push('startup') } })
    expect(order).toEqual(['exchange','session','start','startup']); expect(input.value).toBe('')
  })
})
