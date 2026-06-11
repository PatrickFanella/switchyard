const { authHeader, listSessions, getSession, sendPrompt } = require("./opencode")

async function handleSessions(req, res, opts, helpers) {
  try {
    const authorization = (opts.authHeader || authHeader)(opts)
    const sessions = await (opts.listSessions || listSessions)({ ...opts, authorization })
    opts.observability.counters.sessionPollsTotal += 1
    helpers.json(res, 200, { sessions })
  } catch (error) {
    helpers.json(res, 500, helpers.errorBody(error.message, "server_error"))
  }
}

async function handleSessionDetail(req, res, opts, helpers, sessionID) {
  try {
    const authorization = (opts.authHeader || authHeader)(opts)
    const session = await (opts.getSession || getSession)({ ...opts, authorization }, sessionID)
    opts.observability.counters.sessionWatchesTotal += 1
    helpers.json(res, 200, { session, mode: session.observeOnly ? "observe_only" : "interactive", transport: "polling" })
  } catch (error) {
    helpers.json(res, 500, helpers.errorBody(error.message, "server_error"))
  }
}

async function handleSessionMessage(req, res, opts, helpers, sessionID, requestContext = {}) {
  let body
  try {
    body = await helpers.readJson(req)
  } catch (error) {
    helpers.json(res, 400, helpers.errorBody(error.message))
    return
  }

  if (!body.prompt || typeof body.prompt !== "string") {
    helpers.json(res, 400, helpers.errorBody("prompt is required"))
    return
  }

  try {
    const authorization = (opts.authHeader || authHeader)(opts)
    const result = await (opts.sendPrompt || sendPrompt)({
      ...opts,
      authorization,
      sessionID,
      prompt: body.prompt,
      providerID: body.providerID || opts.defaultProviderID,
      modelID: body.modelID || opts.defaultModelID,
      system: body.system,
      requestID: requestContext.requestID,
      signal: requestContext.signal,
    })
    helpers.json(res, 200, result, { "x-ocq-session": result.sessionID })
  } catch (error) {
    helpers.json(res, 500, helpers.errorBody(error.message, "server_error"))
  }
}

module.exports = { handleSessions, handleSessionDetail, handleSessionMessage }
