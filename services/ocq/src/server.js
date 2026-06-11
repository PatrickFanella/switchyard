const http = require("node:http")
const { randomUUID } = require("node:crypto")
const {
  DEFAULT_BASE_URL,
  DEFAULT_PROVIDER,
  DEFAULT_MODEL,
  DEFAULT_DIRECTORY,
  DEFAULT_ENV_FILE,
  authHeader,
  sendPrompt,
} = require("./opencode")
const { parseModelName, messagesToPrompt, chatCompletionResponse, streamTextChunks } = require("./openai")
const { hasValidBearer } = require("./auth")
const { createObservabilityState } = require("./observability")
const { prometheusMetrics } = require("./metrics")
const { startSse, writeSse, endSse } = require("./sse")
const { handleSessions, handleSessionDetail, handleSessionMessage } = require("./session-api")

const DEFAULT_GATEWAY_HOST = "127.0.0.1"
const DEFAULT_GATEWAY_PORT = 8088
const MAX_BODY_BYTES = 1024 * 1024

function getServerOptions(overrides = {}) {
  const defaultProvider = process.env.OCQ_PROVIDER || DEFAULT_PROVIDER
  const defaultModel = overrides.defaultModel || process.env.OCQ_DEFAULT_MODEL || process.env.OCQ_MODEL || `${defaultProvider}/${DEFAULT_MODEL}`
  const parsedDefault = parseModelName(defaultModel, defaultProvider, DEFAULT_MODEL)
  return {
    host: overrides.host || process.env.OCQ_GATEWAY_HOST || DEFAULT_GATEWAY_HOST,
    port: Number(overrides.port || process.env.OCQ_GATEWAY_PORT || DEFAULT_GATEWAY_PORT),
    gatewayKey: overrides.gatewayKey || process.env.OCQ_GATEWAY_KEY,
    baseUrl: (overrides.baseUrl || process.env.OPENCODE_SERVER_URL || DEFAULT_BASE_URL).replace(/\/+$/, ""),
    envFile: overrides.envFile || process.env.OCQ_ENV_FILE || DEFAULT_ENV_FILE,
    directory: overrides.directory || process.env.OCQ_DEFAULT_DIRECTORY || process.env.OCQ_DIRECTORY || DEFAULT_DIRECTORY,
    defaultProviderID: parsedDefault.providerID,
    defaultModelID: parsedDefault.modelID,
    defaultModel: parsedDefault.model,
  }
}

function json(res, status, body, headers = {}) {
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    ...headers,
  })
  res.end(`${JSON.stringify(body)}\n`)
}

function errorBody(message, type = "invalid_request_error", code) {
  return { error: { message, type, code } }
}

function requestIDFrom(req) {
  return req.headers["x-request-id"] || req.headers["x-llama-line-request-id"] || `ocq-${randomUUID()}`
}

function createInFlightRegistry() {
  const controllers = new Map()

  return {
    register(requestID, controller) {
      controllers.set(requestID, controller)
    },
    unregister(requestID, controller) {
      if (controllers.get(requestID) === controller) controllers.delete(requestID)
    },
    abort(requestID) {
      const controller = controllers.get(requestID)
      if (!controller) return null
      const wasAborted = controller.signal.aborted
      if (!wasAborted) controller.abort()
      return { controller, wasAborted }
    },
  }
}

function createTrackedRequestContext(req, res, opts, requestID) {
  const controller = new AbortController()
  let cleaned = false
  const abort = () => {
    if (!res.writableEnded) controller.abort()
  }
  const cleanup = () => {
    if (cleaned) return
    cleaned = true
    req.off("aborted", abort)
    res.off("close", abort)
    res.off("finish", cleanup)
    opts.inflightRequests.unregister(requestID, controller)
  }
  req.on("aborted", abort)
  res.on("close", abort)
  res.once("finish", cleanup)
  opts.inflightRequests.register(requestID, controller)

  return {
    signal: controller.signal,
    cleanup,
  }
}

function requireBearer(req, res, opts) {
  if (hasValidBearer(req, opts.gatewayKey)) return true
  json(res, 401, errorBody("missing or invalid bearer token", "authentication_error", "unauthorized"), {
    "www-authenticate": "Bearer",
  })
  return false
}

function readJson(req) {
  return new Promise((resolve, reject) => {
    let size = 0
    const chunks = []
    req.on("data", (chunk) => {
      size += chunk.length
      if (size > MAX_BODY_BYTES) {
        reject(new Error("request body too large"))
        req.destroy()
        return
      }
      chunks.push(chunk)
    })
    req.on("error", reject)
    req.on("end", () => {
      try {
        const text = Buffer.concat(chunks).toString("utf8")
        resolve(text ? JSON.parse(text) : {})
      } catch {
        reject(new Error("invalid JSON body"))
      }
    })
  })
}

async function handleChatCompletions(req, res, opts) {
  const authHeaderFn = opts.authHeader || authHeader
  const sendPromptFn = opts.sendPrompt || sendPrompt
  const requestID = requestIDFrom(req)
  const requestContext = createTrackedRequestContext(req, res, opts, requestID)

  try {
    if (!requireBearer(req, res, opts)) {
      return
    }

    let body
    try {
      body = await readJson(req)
    } catch (error) {
      json(res, 400, errorBody(error.message))
      return
    }

    let model
    let prompt
    try {
      model = parseModelName(body.model || opts.defaultModel, opts.defaultProviderID, opts.defaultModelID)
      prompt = messagesToPrompt(body.messages)
    } catch (error) {
      json(res, 400, errorBody(error.message))
      return
    }

    if (body.stream === true) {
      opts.observability.counters.activeStreams += 1
      try {
        const authorization = authHeaderFn(opts)
        const result = await sendPromptFn({
          baseUrl: opts.baseUrl,
          directory: opts.directory,
          envFile: opts.envFile,
          authorization,
          providerID: model.providerID,
          modelID: model.modelID,
          prompt: prompt.prompt,
          system: prompt.system,
          sessionID: body.ocq_session_id,
          signal: requestContext.signal,
          requestID,
        })
        res.setHeader("x-ocq-session", result.sessionID)
        res.setHeader("x-ocq-request-id", requestID)
        res.setHeader("x-ocq-status", "completed")
        res.setHeader("x-ocq-streaming-mode", "buffered")
        startSse(res)
        for (const chunk of streamTextChunks(result.text, model.model)) {
          writeSse(res, chunk)
        }
        writeSse(res, "[DONE]")
        endSse(res)
      } catch (error) {
        if (!res.headersSent) {
          json(res, 500, errorBody(error.message, "server_error"))
        } else if (!res.writableEnded) {
          endSse(res)
        }
      } finally {
        opts.observability.counters.activeStreams -= 1
      }
      return
    }

    const authorization = authHeaderFn(opts)
    const result = await sendPromptFn({
      baseUrl: opts.baseUrl,
      directory: opts.directory,
      envFile: opts.envFile,
      authorization,
      providerID: model.providerID,
      modelID: model.modelID,
      prompt: prompt.prompt,
      system: prompt.system,
      sessionID: body.ocq_session_id,
      signal: requestContext.signal,
      requestID,
    })
    json(res, 200, chatCompletionResponse({ model: model.model, text: result.text, sessionID: result.sessionID }), {
      "x-ocq-session": result.sessionID,
      "x-ocq-request-id": requestID,
      "x-ocq-status": "completed",
    })
  } catch (error) {
    if (error?.name === "AbortError" || requestContext.signal.aborted) {
      if (!res.headersSent && !res.writableEnded) {
        json(res, 500, errorBody("request aborted", "server_error", "aborted"))
      }
      return
    }
    json(res, 500, errorBody(error.message, "server_error"))
  }
}

async function handleRequestAbort(req, res, opts, requestID) {
  const result = opts.inflightRequests.abort(requestID)
  if (!result) {
    json(res, 404, errorBody("request not found", "invalid_request_error", "not_found"))
    return
  }

  json(res, result.wasAborted ? 200 : 202, { requestID, aborted: true })
}

function createGatewayServer(options = {}) {
  const opts = getServerOptions(options)
  opts.observability = options.observability || createObservabilityState()
  opts.inflightRequests = options.inflightRequests || createInFlightRegistry()
  opts.authHeader = options.authHeader
  opts.sendPrompt = options.sendPrompt
  opts.listSessions = options.listSessions
  opts.getSession = options.getSession
  return http.createServer(async (req, res) => {
    const url = new URL(req.url, `http://${req.headers.host || `${opts.host}:${opts.port}`}`)
    const startedAt = process.hrtime.bigint()

    res.on("finish", () => {
      const durationMs = Number(process.hrtime.bigint() - startedAt) / 1e6
      opts.observability.recordRequest({
        method: req.method,
        path: url.pathname,
        status: res.statusCode,
        durationMs,
        headers: req.headers,
      })
      if (res.statusCode >= 500) {
        opts.observability.log("error", `${req.method} ${url.pathname} -> ${res.statusCode}`, {
          method: req.method,
          path: url.pathname,
          status: res.statusCode,
        })
      }
    })

    if (req.method === "GET" && (url.pathname === "/health" || url.pathname === "/healthz")) {
      json(res, 200, { ok: true })
      return
    }

    if (url.pathname.startsWith("/ocq/") || url.pathname === "/metrics") {
      if (!requireBearer(req, res, opts)) return
    }

    if (req.method === "GET" && url.pathname === "/ocq/metrics") {
      json(res, 200, opts.observability.summary())
      return
    }

    if (req.method === "GET" && url.pathname === "/ocq/requests") {
      json(res, 200, { requests: opts.observability.requests.list() })
      return
    }

    if (req.method === "GET" && url.pathname === "/ocq/logs") {
      json(res, 200, { logs: opts.observability.logs.list() })
      return
    }

    const requestAbortMatch = url.pathname.match(/^\/ocq\/requests\/([^/]+)\/abort$/)
    const sessionMessageAbortMatch = url.pathname.match(/^\/ocq\/sessions\/([^/]+)\/messages\/([^/]+)\/abort$/)

    const sessionDetailMatch = url.pathname.match(/^\/ocq\/sessions\/([^/]+)$/)
    const sessionMessageMatch = url.pathname.match(/^\/ocq\/sessions\/([^/]+)\/messages$/)

    if (req.method === "GET" && url.pathname === "/ocq/sessions") {
      await handleSessions(req, res, opts, { json, readJson, errorBody })
      return
    }

    if (sessionDetailMatch && req.method === "GET") {
      await handleSessionDetail(req, res, opts, { json, readJson, errorBody }, decodeURIComponent(sessionDetailMatch[1]))
      return
    }

    if (sessionMessageMatch && req.method === "POST") {
      const requestID = requestIDFrom(req)
      const requestContext = createTrackedRequestContext(req, res, opts, requestID)
      await handleSessionMessage(req, res, opts, { json, readJson, errorBody }, decodeURIComponent(sessionMessageMatch[1]), {
        requestID,
        signal: requestContext.signal,
      })
      return
    }

    if (requestAbortMatch && req.method === "POST") {
      await handleRequestAbort(req, res, opts, decodeURIComponent(requestAbortMatch[1]))
      return
    }

    if (sessionMessageAbortMatch && req.method === "POST") {
      await handleRequestAbort(req, res, opts, decodeURIComponent(sessionMessageAbortMatch[2]))
      return
    }

    if (req.method === "GET" && url.pathname === "/metrics") {
      res.writeHead(200, { "content-type": "text/plain; version=0.0.4; charset=utf-8" })
      res.end(prometheusMetrics(opts.observability))
      return
    }

    if (req.method === "POST" && url.pathname === "/v1/chat/completions") {
      await handleChatCompletions(req, res, opts)
      return
    }

    json(res, 404, errorBody("not found", "invalid_request_error", "not_found"))
  })
}

function listen(options = {}) {
  const opts = getServerOptions(options)
  if (!opts.gatewayKey) throw new Error("OCQ_GATEWAY_KEY is required")
  const server = createGatewayServer(opts)
  server.listen(opts.port, opts.host, () => {
    process.stdout.write(`ocq gateway listening on http://${opts.host}:${opts.port}\n`)
  })
  return server
}

module.exports = {
  DEFAULT_GATEWAY_HOST,
  DEFAULT_GATEWAY_PORT,
  getServerOptions,
  createGatewayServer,
  listen,
}
