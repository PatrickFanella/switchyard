const fs = require("node:fs")
const os = require("node:os")
const path = require("node:path")

const DEFAULT_BASE_URL = "http://127.0.0.1:4096"
const DEFAULT_PROVIDER = "openai"
const DEFAULT_MODEL = "gpt-5.4-mini"
const DEFAULT_DIRECTORY = os.homedir()
const DEFAULT_ENV_FILE = path.join(os.homedir(), ".config/opencode/server.env")
const DEFAULT_SYSTEM = "Use browser tools when needed for current web info or page interaction. Otherwise answer directly and concisely."

class OpenCodeError extends Error {
  constructor(message, detail) {
    super(message)
    this.name = "OpenCodeError"
    this.detail = detail
  }
}

function parseEnvFile(file) {
  let text
  try {
    text = fs.readFileSync(file, "utf8")
  } catch (error) {
    throw new OpenCodeError(`cannot read env file: ${file}`, error.message)
  }

  const env = {}
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith("#")) continue

    const match = line.match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/)
    if (!match) continue

    let value = match[2].trim()
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1)
    }
    env[match[1]] = value
  }
  return env
}

function authHeader(opts) {
  const processUsername = process.env.OPENCODE_SERVER_USERNAME
  const processPassword = process.env.OPENCODE_SERVER_PASSWORD
  if (processUsername && processPassword) {
    return `Basic ${Buffer.from(`${processUsername}:${processPassword}`).toString("base64")}`
  }
  const env = parseEnvFile(opts.envFile)
  const username = process.env.OPENCODE_SERVER_USERNAME || env.OPENCODE_SERVER_USERNAME
  const password = process.env.OPENCODE_SERVER_PASSWORD || env.OPENCODE_SERVER_PASSWORD
  if (!username || !password) {
    throw new OpenCodeError(`missing OPENCODE_SERVER_USERNAME or OPENCODE_SERVER_PASSWORD in ${opts.envFile}`)
  }
  return `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`
}

async function request(opts, method, pathname, body) {
  const url = new URL(pathname, opts.baseUrl)
  url.searchParams.set("directory", opts.directory)

  const response = await fetch(url, {
    method,
    signal: opts.signal,
    headers: {
      authorization: opts.authorization,
      "content-type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  const text = await response.text()
  let data
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = text
  }

  if (!response.ok) {
    const detail = typeof data === "string" ? data : JSON.stringify(data, null, 2)
    const error = new OpenCodeError(`${method} ${url.pathname} failed: HTTP ${response.status}`, detail)
    error.status = response.status
    error.response = data
    throw error
  }
  return data
}

async function createSession(opts) {
  const body = {}
  if (opts.title) body.title = opts.title
  const session = await request(opts, "POST", "/session", body)
  if (!session?.id) throw new OpenCodeError("server did not return a session id")
  return session.id
}

function normalizeSession(session) {
  return {
    id: session?.id,
    title: session?.title || session?.id,
    createdAt: session?.time?.created || session?.createdAt || null,
    updatedAt: session?.time?.updated || session?.updatedAt || null,
    observeOnly: true,
  }
}

function textFromParts(parts) {
  return (parts || [])
    .filter((part) => part?.type === "text" && typeof part.text === "string")
    .map((part) => part.text)
    .join("\n")
    .trim()
}

async function sendPrompt(opts) {
  const sessionID = opts.sessionID || await createSession(opts)
  const body = {
    model: {
      providerID: opts.providerID,
      modelID: opts.modelID,
    },
    parts: [
      {
        type: "text",
        text: opts.prompt,
      },
    ],
  }
  if (opts.system) body.system = opts.system
  if (opts.agent) body.agent = opts.agent

  const result = await request(opts, "POST", `/session/${encodeURIComponent(sessionID)}/message`, body)
  const text = textFromParts(result?.parts)
  if (!text && result?.info?.error) {
    throw new OpenCodeError("assistant returned an error", JSON.stringify(result.info.error, null, 2))
  }
  return { sessionID, messageID: result?.info?.id, text }
}

function normalizeMessage(message) {
  return {
    id: message?.id || message?.info?.id || null,
    role: message?.role || message?.info?.role || "assistant",
    content: textFromParts(message?.parts || (typeof message?.content === "string" ? [{ type: "text", text: message.content }] : [])),
    raw: message,
  }
}

async function listSessions(opts) {
  const sessions = await request(opts, "GET", "/session")
  const list = Array.isArray(sessions) ? sessions : sessions?.sessions || []
  return list.map(normalizeSession)
}

async function getSession(opts, sessionID) {
  const session = await request(opts, "GET", `/session/${encodeURIComponent(sessionID)}`)
  return {
    ...normalizeSession(session),
    messages: (session?.messages || session?.children || []).map(normalizeMessage),
  }
}

module.exports = {
  DEFAULT_BASE_URL,
  DEFAULT_PROVIDER,
  DEFAULT_MODEL,
  DEFAULT_DIRECTORY,
  DEFAULT_ENV_FILE,
  DEFAULT_SYSTEM,
  OpenCodeError,
  parseEnvFile,
  authHeader,
  request,
  createSession,
  normalizeSession,
  normalizeMessage,
  textFromParts,
  listSessions,
  getSession,
  sendPrompt,
}
