/**
 * Plugin logging for the AgentHub OpenCode plugin.
 *
 * OpenCode's plugin context (see `@opencode/plugin`) has no logger field, so
 * plugin output goes to the server log through `console.log`. The `Logger`
 * type exists so tests (and future embedders) can capture messages instead of
 * scraping stdout.
 */
export type Logger = (message: string) => void

/** Default logger: writes one line to the OpenCode server log. */
export const consoleLogger: Logger = (message) => {
  console.log(message)
}
