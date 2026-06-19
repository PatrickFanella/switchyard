package controlplane

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

type modelBrokerClientIdentity struct {
	Name     string
	Priority int
}

type modelBrokerClientConfig struct {
	ByToken map[string]modelBrokerClientIdentity
	ByName  map[string]int
}

func modelBrokerClientIdentityFromRequest(r *http.Request) modelBrokerClientIdentity {
	config := parseModelBrokerClientConfig()
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		if identity, ok := config.ByToken[token]; ok {
			return identity
		}
	}
	name := firstNonEmpty(
		r.Header.Get("X-Switchyard-Client"),
		r.Header.Get("X-Llama-Line-Client"),
		r.Header.Get("X-Client-Name"),
	)
	priority := firstHeaderInt(r.Header, "X-Switchyard-Priority", "X-Llama-Line-Priority", "X-Client-Priority")
	if name != "" {
		if configured, ok := config.ByName[name]; ok && priority == 0 {
			priority = configured
		}
		return modelBrokerClientIdentity{Name: name, Priority: priority}
	}
	client := r.RemoteAddr
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		client = forwarded
	}
	return modelBrokerClientIdentity{Name: client, Priority: priority}
}

func parseModelBrokerClientConfig() modelBrokerClientConfig {
	config := modelBrokerClientConfig{ByToken: map[string]modelBrokerClientIdentity{}, ByName: map[string]int{}}
	raw := firstNonEmpty(os.Getenv("MODEL_BROKER_CLIENT_KEYS"), os.Getenv("SWITCHYARD_MODEL_BROKER_CLIENT_KEYS"))
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		nameToken, priorityText, _ := strings.Cut(entry, ":")
		priority := 0
		if strings.TrimSpace(priorityText) != "" {
			priority, _ = strconv.Atoi(strings.TrimSpace(priorityText))
		}
		name, token, ok := strings.Cut(nameToken, "=")
		if !ok {
			parts := strings.Split(nameToken, ":")
			if len(parts) >= 2 {
				name, token = parts[0], parts[1]
				ok = true
			}
		}
		name = strings.TrimSpace(name)
		token = strings.TrimSpace(token)
		if name == "" {
			continue
		}
		config.ByName[name] = priority
		if ok && token != "" {
			config.ByToken[token] = modelBrokerClientIdentity{Name: name, Priority: priority}
		}
	}
	for _, entry := range strings.FieldsFunc(os.Getenv("MODEL_BROKER_CLIENT_PRIORITIES"), func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		name, priorityText, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		priority, _ := strconv.Atoi(strings.TrimSpace(priorityText))
		config.ByName[strings.TrimSpace(name)] = priority
	}
	return config
}

func bearerToken(authorization string) string {
	fields := strings.Fields(strings.TrimSpace(authorization))
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return fields[1]
	}
	return ""
}

func firstHeaderInt(header http.Header, names ...string) int {
	for _, name := range names {
		if raw := strings.TrimSpace(header.Get(name)); raw != "" {
			value, _ := strconv.Atoi(raw)
			return value
		}
	}
	return 0
}
