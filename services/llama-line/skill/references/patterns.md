# llama-line Client Patterns

All inference responses from llama-line use `Content-Type: text/event-stream`. The stream
always begins with zero or more broker status events, followed by the ollama response:

- **Non-streaming** (`stream:false`): broker heartbeats → one `data: <full JSON>\n\n`
- **Streaming** (`stream:true`): broker heartbeats → ollama SSE chunks → `data: [DONE]\n\n`

## Python — Non-Streaming with SSE Preamble Stripping

```python
import json
import requests

BROKER = "http://localhost:11434"
API_KEY = "your-key-here"

def call_llama_line(model: str, prompt: str) -> dict:
    headers = {
        "Authorization": f"Bearer {API_KEY}",
        "Content-Type": "application/json",
    }
    payload = {"model": model, "prompt": prompt, "stream": False}

    with requests.post(f"{BROKER}/api/generate", json=payload, headers=headers, stream=True) as resp:
        resp.raise_for_status()
        for raw_line in resp.iter_lines(decode_unicode=True):
            if not raw_line:
                continue
            if not raw_line.startswith("data: "):
                continue
            payload_str = raw_line[6:]
            data = json.loads(payload_str)
            status = data.get("status")
            if status == "queued":
                print(f"[queue] pos={data.get('position')} wait={data.get('wait_seconds')}s")
                continue
            if status == "ollama_unavailable":
                raise RuntimeError(f"broker error: {data.get('message', status)}")
            # No status field → this is the actual ollama response
            return data
```

## Python — Streaming with SSE Preamble Stripping

```python
def call_llama_line_stream(model: str, prompt: str):
    headers = {
        "Authorization": f"Bearer {API_KEY}",
        "Content-Type": "application/json",
    }
    payload = {"model": model, "prompt": prompt, "stream": True}

    with requests.post(f"{BROKER}/api/generate", json=payload, headers=headers, stream=True) as resp:
        resp.raise_for_status()
        broker_done = False
        for raw_line in resp.iter_lines(decode_unicode=True):
            if not raw_line or raw_line == "data: [DONE]":
                continue
            if not raw_line.startswith("data: "):
                continue
            data = json.loads(raw_line[6:])
            if not broker_done:
                status = data.get("status")
                if status == "queued":
                    print(f"[queue] pos={data.get('position')} wait={data.get('wait_seconds')}s")
                    continue
                if status == "ollama_unavailable":
                    raise RuntimeError(f"broker error: {data.get('message', status)}")
                broker_done = True
            # Ollama streaming chunk
            print(data.get("response", ""), end="", flush=True)
            if data.get("done"):
                break
```

## Go — SSE Loop with bufio.Scanner

```go
package main

import (
    "bufio"
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "strings"
)

const broker = "http://localhost:11434"
const apiKey = "your-key-here"

type StatusUpdate struct {
    RequestID   string `json:"request_id"`
    Position    int    `json:"position"`
    WaitSeconds int    `json:"wait_seconds"`
    Status      string `json:"status"`
    Message     string `json:"message"`
}

func callLlamaLine(model, prompt string) error {
    body, _ := json.Marshal(map[string]any{
        "model":  model,
        "prompt": prompt,
        "stream": true,
    })
    req, _ := http.NewRequest("POST", broker+"/api/generate", bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+apiKey)
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    scanner := bufio.NewScanner(resp.Body)
    brokerDone := false
    for scanner.Scan() {
        line := scanner.Text()
        if line == "" || line == "data: [DONE]" {
            continue
        }
        if !strings.HasPrefix(line, "data: ") {
            continue
        }
        payload := line[6:]
        if !brokerDone {
            var u StatusUpdate
            if err := json.Unmarshal([]byte(payload), &u); err == nil && u.Status != "" {
                if u.Status == "queued" {
                    fmt.Printf("[queue] pos=%d wait=%ds id=%s\n", u.Position, u.WaitSeconds, u.RequestID)
                    continue
                }
                if u.Status == "ollama_unavailable" {
                    return fmt.Errorf("broker error: %s", u.Message)
                }
            }
            brokerDone = true
        }
        // Ollama chunk (streaming) or full response (non-streaming)
        var chunk map[string]any
        json.Unmarshal([]byte(payload), &chunk)
        fmt.Print(chunk["response"])
        if done, _ := chunk["done"].(bool); done {
            break
        }
    }
    return scanner.Err()
}
```

## JavaScript — fetch with SSE Parsing

```javascript
const BROKER = 'http://localhost:11434';
const API_KEY = 'your-key-here';

async function callLlamaLine(model, prompt, stream = true) {
  const resp = await fetch(`${BROKER}/api/generate`, {
    method: 'POST',
    headers: {
      'Authorization': `Bearer ${API_KEY}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ model, prompt, stream }),
  });

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let brokerDone = false;
  let buf = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    const lines = buf.split('\n');
    buf = lines.pop(); // keep incomplete line

    for (const line of lines) {
      if (!line || line === 'data: [DONE]') continue;
      if (!line.startsWith('data: ')) continue;
      const data = JSON.parse(line.slice(6));
      if (!brokerDone) {
        if (data.status === 'queued') {
          console.log('[queue]', data);
          continue;
        }
        if (data.status === 'ollama_unavailable') {
          throw new Error(`broker error: ${data.message}`);
        }
        brokerDone = true;
      }
      // Ollama chunk or full response
      process.stdout.write(data.response ?? '');
      if (data.done) return;
    }
  }
}
```

## OpenAI SDK Integration (Python)

The OpenAI Python client handles the SSE preamble transparently for **streaming** requests
because it reads `data:` lines natively. For **non-streaming**, the broker wraps the response
in a `data:` line — the SDK handles this correctly too since it parses the SSE stream.

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:11434/v1",
    api_key="your-key-here",  # llama-line key, not OpenAI
)

# Non-streaming — SDK strips SSE preamble automatically
response = client.chat.completions.create(
    model="qwen3.5:latest",
    messages=[{"role": "user", "content": "Hello"}],
)
print(response.choices[0].message.content)

# Streaming — broker heartbeats are discarded; ollama chunks stream through
for chunk in client.chat.completions.create(
    model="qwen3.5:latest",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True,
):
    print(chunk.choices[0].delta.content or "", end="", flush=True)
```

## OpenAI SDK Integration (Go)

For Go, use a custom `http.RoundTripper` to strip broker heartbeats before the SDK sees the body.
See `augr/internal/llm/llamabroker` for a production-ready implementation:

- `StripSSEPreamble` — strips broker events, unwraps the final `data:` line for non-streaming
- `StripBrokerHeartbeats` — strips broker events, passes remaining SSE chunks through for streaming
- Detect streaming by peeking `"stream":true` in the request body via `req.GetBody()`

## Backpressure — Check Before Sending

For batch jobs or high-throughput clients, poll `/broker/status` before sending to avoid 503s:

```python
def wait_for_queue_space(max_depth_fraction=0.8, poll_interval=2.0):
    import time
    while True:
        r = requests.get(f"{BROKER}/broker/status")
        s = r.json()
        if s["queue_depth"] < s["max_depth"] * max_depth_fraction:
            return
        print(f"Queue {s['queue_depth']}/{s['max_depth']}, waiting...")
        time.sleep(poll_interval)
```

## Error Handling

```python
def safe_call(model, prompt):
    try:
        resp = requests.post(
            f"{BROKER}/api/generate",
            json={"model": model, "prompt": prompt, "stream": False},
            headers={"Authorization": f"Bearer {API_KEY}"},
            stream=True,
        )
        if resp.status_code == 503:
            err = resp.json()
            raise Exception(f"Queue full ({err['queue_depth']}/{err['max_depth']})")
        if resp.status_code == 504:
            raise Exception(f"Timeout: {resp.json()['error']}")
        if resp.status_code == 401:
            raise Exception("Invalid API key")
        resp.raise_for_status()
        # Strip broker SSE preamble; final response is in a data: line
        for raw_line in resp.iter_lines(decode_unicode=True):
            if not raw_line or not raw_line.startswith("data: "):
                continue
            data = json.loads(raw_line[6:])
            if data.get("status") == "ollama_unavailable":
                raise Exception(f"Broker error: {data.get('message')}")
            if "status" not in data:
                return data["response"]
    except requests.RequestException as e:
        raise Exception(f"Broker unreachable: {e}")
```

## Health Check

```python
def broker_healthy() -> bool:
    try:
        r = requests.get(f"{BROKER}/broker/status", timeout=2)
        return r.status_code == 200
    except Exception:
        return False
```
