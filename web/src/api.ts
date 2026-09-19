export type Session = { provider: string; state: string; reused?: boolean; expires_at?: string; error?: string }
export type Observation = { instrument_id: string; symbol: string; segment: string; latest_price_minor: number; exchange_timestamp: string; ingested_timestamp: string; freshness_state: string }
export type Readiness = { state: string; trading_permitted: boolean; providers?: { id: string; state: string; required: number; covered: number }[] }
export type Runtime = { mode: string; strategy: string; candidate: string; qualification: string; revision: number; brokerOrders: string; status: Underlying[] }
export type Underlying = { underlying: string; market_data: string; future: string; option_universe: string; strategy: string; warmup_samples: number; warmup_required: number }
export type Ready = { status: string; market_data_state: string; trading_permitted: boolean }
export type IntegrationStatus = { stream: { state?: string } }
export type Evaluation = { strategy: string; candidate: string; underlying: string; evaluated_at: string; frame_id: string; decision: string; reason: string }
export type Signal = { Underlying: string; SignalTime: string; Direction: string; OptionID: string; Entry: { PriceMinor: number }; Risk: string; RiskReason: string }
export type QualificationSeries = { Underlying: string; Records?: Signal[]; Open?: { OptionID: string; Quantity: number; EntryMinor: number; CurrentMarkMinor: number }; Trades?: { GrossPnLMinor: number }[] }
type RuntimeResponse = { Mode?: string; Strategy?: string; Candidate?: string; Qualification?: string; Revision?: number; BrokerOrders?: string; Status?: Underlying[] }

export class APIError extends Error { constructor(message: string) { super(message) } }
export function integrationDisplayState(sessionState: string | undefined, status: IntegrationStatus | undefined, unavailable: boolean): string {
  if (sessionState === 'LOGIN_REQUIRED') return 'OFFLINE'
  if (unavailable || status === undefined) return 'UNAVAILABLE'
  return status.stream.state === 'CONNECTED' ? 'ONLINE' : 'OFFLINE'
}
async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), 5_000)
  try {
    const response = await fetch(url, { ...init, signal: controller.signal, headers: { 'Content-Type': 'application/json', ...init?.headers } })
    const payload = await response.json().catch(() => ({}))
    if (!response.ok) throw new APIError(payload.error ?? `HTTP ${response.status}`)
    return payload as T
  } catch (error) {
    if (error instanceof APIError) throw error
    throw new APIError(error instanceof DOMException && error.name === 'AbortError' ? 'request timed out' : 'service unavailable')
  } finally { window.clearTimeout(timeout) }
}
export const api = {
  session: () => request<Session>('/control/api/v1/session/status'),
  loginURL: () => request<{ login_url: string }>('/control/api/v1/session/login-url'),
  exchange: (requestToken: string) => request<Session>('/control/api/v1/session/exchange', { method: 'POST', body: JSON.stringify({ request_token: requestToken }) }),
  observations: () => request<{ items: Observation[] }>('/shadow/api/v1/market-data/observations/latest'),
  marketReadiness: () => request<Readiness>('/shadow/api/v1/market-data/readiness'),
  runtime: async (): Promise<Runtime> => {
    const response = await request<RuntimeResponse>('/shadow/api/v1/shadow/runtime')
    return {
      mode: response.Mode ?? 'UNAVAILABLE', strategy: response.Strategy ?? 'UNAVAILABLE',
      candidate: response.Candidate ?? 'UNAVAILABLE', qualification: response.Qualification ?? 'UNAVAILABLE',
      revision: response.Revision ?? 0, brokerOrders: response.BrokerOrders ?? 'UNAVAILABLE',
      status: Array.isArray(response.Status) ? response.Status : [],
    }
  },
  readiness: () => request<Ready>('/shadow/readyz'),
  integrationStatus: () => request<IntegrationStatus>('/shadow/api/v1/integrations/zerodha/status'),
  evaluations: () => request<{ items: Evaluation[]; count: number }>('/shadow/api/v1/shadow/evaluations?limit=100'),
  signals: () => request<Signal[] | null>('/shadow/api/v1/qualification/signals/recent?limit=100'),
  qualification: () => request<QualificationSeries[]>('/shadow/api/v1/qualification/strategies'),
}
