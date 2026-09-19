import { launch } from './dev-lib.mjs'

launch().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error))
  process.exitCode = 1
})
