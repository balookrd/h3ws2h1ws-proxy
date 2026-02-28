# h3ws2h1ws-proxy

Прокси-сервер для WebSocket over HTTP/3 (RFC 9220) на входе и классического WebSocket over HTTP/1.1 на backend.

## Что умеет проект

- Принимать WebSocket-подключения через **HTTP/3 extended CONNECT**.
- Проксировать трафик между клиентом (H3 WS) и backend-сервисом (`ws://` или `wss://`).
- Ограничивать размер:
  - отдельного WebSocket frame (`max-frame`),
  - итогового сообщения (`max-message`).
- Ограничивать общее число одновременных сессий (`max-conns`).
- Проксировать control frames (`ping`, `pong`, `close`) и корректно завершать сессии.
- Отдавать Prometheus-метрики на отдельном HTTP endpoint (`/metrics`).

## Архитектура и назначение модулей

### `main.go`
Точка входа приложения:
- парсинг флагов,
- валидация backend URL,
- запуск metrics endpoint,
- создание и запуск HTTP/3 сервера.

### `config.go`
Содержит:
- `Config` — конфигурация процесса (адреса, лимиты, таймауты),
- `Limits` — runtime-лимиты прокси,
- `defaultTLSConfig()` — TLS 1.3 + ALPN для HTTP/3.

### `metrics.go`
Определяет и регистрирует Prometheus-метрики:
- активные сессии,
- принятые/отклонённые подключения,
- ошибки по стадиям,
- объём и число проксируемых сообщений,
- control frames,
- дропы из-за лимитов.

### `proxy.go`
Содержит основную логику установки сессии:
- проверка RFC9220-заголовков,
- ответ handshake (`Sec-WebSocket-Accept`),
- dial backend WS,
- запуск двунаправленных pump-потоков,
- жизненный цикл сессии и обработка ошибок.

### `pumps.go`
Логика передачи данных:
- `pumpH3ToBackend` — из H3-потока в backend WebSocket,
- `pumpBackendToH3` — из backend WebSocket в H3-поток.

Включает:
- сборку фрагментированных сообщений,
- применение таймаутов,
- реакцию на `ping/pong/close`,
- проверки лимитов размеров.

### `ws_framing.go`
Низкоуровневая реализация RFC6455:
- чтение frame (`readWSFrame`),
- запись data/control/close frame,
- фрагментация больших payload,
- маскирование/демаскирование,
- парсинг payload close frame.

### `utils.go`
Вспомогательные функции:
- `computeAccept` — расчёт `Sec-WebSocket-Accept`,
- `pickFirstToken` — выбор первого subprotocol,
- `isNetClose` — эвристика «нормального» закрытия соединения.

## Запуск

### Требования
- Go 1.22+
- TLS-сертификат и ключ (`cert.pem`, `key.pem`) для HTTP/3 сервера.

### Пример

```bash
go run . \
  -listen :443 \
  -cert cert.pem \
  -key key.pem \
  -backend ws://127.0.0.1:8080/ws \
  -path /ws \
  -metrics 127.0.0.1:9090
```

## Основные флаги

- `-listen` — UDP адрес HTTP/3 сервера (по умолчанию `:443`)
- `-cert` / `-key` — TLS сертификат и ключ
- `-backend` — URL backend WebSocket (`ws://` или `wss://`)
- `-path` — путь для RFC9220 CONNECT (по умолчанию `/ws`)
- `-metrics` — адрес endpoint метрик (по умолчанию `127.0.0.1:9090`)
- `-max-frame` — максимум байт в одном frame
- `-max-message` — максимум байт в одном собранном сообщении
- `-max-conns` — максимум одновременных сессий
- `-read-timeout` / `-write-timeout` — таймауты чтения/записи

## Метрики

Endpoint: `http://<metrics-addr>/metrics`

Ключевые метрики:
- `h3ws_proxy_active_sessions`
- `h3ws_proxy_accepted_total`
- `h3ws_proxy_rejected_total{reason=...}`
- `h3ws_proxy_errors_total{stage=...}`
- `h3ws_proxy_bytes_total{dir=...}`
- `h3ws_proxy_messages_total{dir=...,type=...}`
- `h3ws_proxy_control_frames_total{type=...}`
- `h3ws_proxy_oversize_drops_total{kind=...}`
