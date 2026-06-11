const { randomUUID } = require("node:crypto")

function parseModelName(model, fallbackProviderID, fallbackModelID) {
  const value = model || `${fallbackProviderID}/${fallbackModelID}`
  const slash = value.indexOf("/")
  if (slash < 0 && fallbackProviderID) {
    return {
      providerID: fallbackProviderID,
      modelID: value || fallbackModelID,
      model: `${fallbackProviderID}/${value || fallbackModelID}`,
    }
  }
  if (slash <= 0 || slash === value.length - 1) {
    throw new Error("model must be provider/model")
  }
  return {
    providerID: value.slice(0, slash),
    modelID: value.slice(slash + 1),
    model: value,
  }
}

function normalizeContent(content) {
  if (typeof content === "string") return content
  if (Array.isArray(content)) {
    return content
      .map((part) => {
        if (typeof part === "string") return part
        if (part?.type === "text" && typeof part.text === "string") return part.text
        return ""
      })
      .filter(Boolean)
      .join("\n")
  }
  return content == null ? "" : String(content)
}

function messagesToPrompt(messages) {
  if (!Array.isArray(messages) || messages.length === 0) {
    throw new Error("messages must be a non-empty array")
  }

  const system = []
  const prompt = []
  for (const message of messages) {
    const role = message?.role
    const text = normalizeContent(message?.content).trim()
    if (!role || !text) continue
    if (role === "system") {
      system.push(text)
    } else {
      prompt.push(`${role}: ${text}`)
    }
  }

  if (prompt.length === 0) throw new Error("messages must include non-system content")
  return {
    system: system.length ? system.join("\n\n") : undefined,
    prompt: prompt.join("\n\n"),
  }
}

function chatCompletionResponse({ model, text, sessionID }) {
  return {
    id: `chatcmpl-${randomUUID()}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model,
    ...(sessionID ? { ocq_session_id: sessionID } : {}),
    choices: [
      {
        index: 0,
        message: { role: "assistant", content: text || "" },
        finish_reason: "stop",
      },
    ],
  }
}

function chatCompletionChunk({ id, model, delta = {}, finishReason = null }) {
  return {
    id: id || `chatcmpl-${randomUUID()}`,
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [
      {
        index: 0,
        delta,
        finish_reason: finishReason,
      },
    ],
  }
}

function streamTextChunks(text, model) {
  const value = text == null ? "" : String(text)
  const id = `chatcmpl-${randomUUID()}`
  const chunks = [chatCompletionChunk({ id, model, delta: { role: "assistant" } })]
  const parts = value.match(/\s+|\S+/g) || []
  for (const part of parts) {
    chunks.push(chatCompletionChunk({ id, model, delta: { content: part } }))
  }
  chunks.push(chatCompletionChunk({ id, model, delta: {}, finishReason: "stop" }))
  return chunks
}

module.exports = {
  parseModelName,
  messagesToPrompt,
  chatCompletionResponse,
  chatCompletionChunk,
  streamTextChunks,
}
