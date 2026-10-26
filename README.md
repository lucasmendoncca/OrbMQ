# OrbMQ

OrbMQ is a lightweight MQTT broker written in Go. The project currently focuses on a clean and correct MQTT 3.1.1 implementation, while evolving toward a more production-oriented broker with MQTT 5 support, stronger performance, cluster capabilities, and a management interface.

The repository is intentionally being developed in incremental steps. The current codebase is simpler than the long-term vision, and the roadmap below should not be read as already implemented functionality.

## Current Status

OrbMQ currently provides a functional MQTT 3.1.1 publish/subscribe flow with:

- topic subscriptions with wildcard support
- retained message handling
- QoS 0 and QoS 1 delivery, including PUBACK handling and replay of unacknowledged outbound QoS 1 messages on reconnect
- in-memory sessions that restore subscriptions and pending QoS 1 messages when reconnecting with the same Client ID and `CleanSession=false`
- Last Will publication on unexpected disconnects
- Keep Alive timeout enforcement
- concurrent fan-out
- buffered client writes with backpressure handling
- a modular internal architecture around broker, protocol, topic, server, client, and session responsibilities

Current packet support:

| Packet      | Direction        |
|-------------|------------------|
| CONNECT     | Client -> Broker |
| CONNACK     | Broker -> Client |
| PUBLISH     | Both             |
| PUBACK      | Both             |
| SUBSCRIBE   | Client -> Broker |
| SUBACK      | Broker -> Client |
| UNSUBSCRIBE | Client -> Broker |
| UNSUBACK    | Broker -> Client |
| PINGREQ     | Client -> Broker |
| PINGRESP    | Broker -> Client |
| DISCONNECT  | Client -> Broker |

Not implemented yet as complete broker capabilities:

- MQTT 5 support
- durable session storage across process restarts
- session expiry
- QoS 2 flows
- TLS, authentication, and authorization
- clustering
- management API and UI

Session state and retained messages are stored only in memory and are lost when the process exits. With `CleanSession=true`, previous session state is discarded and the new session is removed on disconnect. With `CleanSession=false`, subscriptions and unacknowledged outbound QoS 1 messages survive disconnects within the same broker process; pending messages are replayed with `DUP=true` on reconnect. New outbound QoS 1 deliveries are queued while persistent sessions are offline. Unsent deliveries start with DUP=false; previously attempted deliveries resume with the same Packet ID and DUP=true, in original send order.

Subscriptions requesting QoS 2 are granted QoS 1. The QoS 2 publish handshake is not implemented.

## Project Direction

OrbMQ is being evolved with four main goals:

- **MQTT 5 support** with proper version-aware parsing, properties, reason codes, and compatibility with MQTT 3.1.1 clients
- **Higher performance** through benchmark-driven optimization, lower allocations, efficient routing, and predictable backpressure
- **Cluster readiness** so multiple nodes can cooperate without compromising the standalone broker design
- **Operational management** through a stable management API and a dedicated user interface

The project prioritizes correctness and clarity first, then scales features and performance from that foundation.

## Architecture Overview

OrbMQ is structured to keep protocol logic, broker behavior, and connection handling clearly separated:

- `cmd/orbmq/`
  - application entry point
  - wires broker, session store, and server
  - handles process lifecycle and shutdown
- `internal/server/`
  - TCP listener
  - connection lifecycle management
  - packet routing between clients and broker logic
- `internal/broker/`
  - publish/subscribe coordination
  - retained message management
  - fan-out and backpressure-related behavior
- `internal/client/`
  - connection abstraction over `net.Conn`
  - buffered asynchronous writes
  - client-side queue handling
- `internal/protocol/`
  - MQTT packet encoding and decoding
  - strict binary parsing
  - protocol details without business logic
- `internal/session/`
  - in-memory session state keyed by Client ID
  - subscription state and outbound packet identifier allocation
  - tracking, acknowledgement, and replay of outbound QoS 1 messages
- `internal/topic/`
  - topic tree and wildcard matching
  - subscriber lookup for publish routing

## Design Characteristics

Incoming packets are limited to 1 MiB by default, including the fixed header.
Set `server.WithMaxPacketSize(bytes)` when constructing the server to change this
limit (from 1 byte through `protocol.MaxPacketSize`, 268,435,460 bytes). Oversized
packets close the connection as soon as the header is decoded, before allocating
the body. The limit applies to CONNECT and all subsequent incoming packets; it
does not impose an outbound delivery limit. `protocol.Decode` retains the MQTT
protocol maximum; callers can use `protocol.DecodeWithLimit` for a smaller limit.

MQTT text fields require well-formed UTF-8 and reject U+0000. Topic names must be
nonempty and cannot contain wildcards. Filters require `+` to occupy a complete
level and `#` to occupy the final complete level. Empty levels remain valid;
topics are not normalized, and binary passwords and payloads are preserved.
Filters beginning with `+` or `#` do not match topics beginning with `$`; use an
explicit filter such as `$SYS/#` for those topics. These rules follow the
[MQTT 3.1.1 specification](https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html).

Some notable implementation choices in the current broker:

- **Copy-on-write topic tree**
  - topic updates clone and swap the trie, keeping publish reads lock-free
- **Per-client send queue**
  - each client has a buffered queue and dedicated write loop
  - control responses and publications share the same FIFO queue; only the write loop writes to the connection
  - control responses wait for their write to finish, preserving responses before a subsequent DISCONNECT
  - subscriptions belong to sessions and remain routable during disconnect and reconnect; CONNACK precedes replay, followed by unsent deliveries
  - SUBACK is written before retained messages are delivered
  - transport failures close the affected connection without deleting persistent subscriptions or accepted QoS 1 deliveries
- **Subscriber abstraction**
  - the broker depends on an interface instead of a concrete client type
- **Connection lifecycle and ownership**
  - CONNECT parsing and the initial CONNACK write have a 10-second deadline, configurable with `server.WithHandshakeTimeout`
  - cancellation closes the listener and all connections, including incomplete handshakes; `Start` waits for connection handlers and writers to exit
  - shutdown interrupts pending writes rather than draining queues; persistent in-memory sessions retain unacknowledged QoS 1 messages
  - replacing a Client ID closes its previous connection and waits for its cleanup before opening the new session
  - a shared registry read lock keeps fan-out admission within one session ownership lifetime; an active delivery consumer runs alongside the packet reader and exits before connection ownership is released
- **Allocation-aware publish path**
  - pooling is used to reduce pressure on hot-path subscriber matching

These patterns should continue to guide future work, especially when extending the broker for MQTT 5 and cluster scenarios.

## Getting Started

### Requirements

- Go 1.25.0 or newer
- an MQTT client such as MQTT Explorer or `mosquitto_pub` / `mosquitto_sub`

### Run the broker

```sh
go run ./cmd/orbmq/main.go
```

The executable loads the required `config.yaml` from its working directory and listens on `:1883` (all interfaces) by default. Use `go run ./cmd/orbmq/main.go -config path/to/config.yaml` to select another file. Configuration is loaded once before listening; there is no reload or environment override. Connections use plain TCP without authentication or authorization; CONNECT credentials are parsed but not authenticated.

### Configuration and session capacity

The root `config.yaml` configures the listening address, handshake timeout,
incoming packet limit, session budgets and observation intervals. Missing fields
use documented defaults; a missing file, unknown fields, nulls, aliases, multiple
documents or invalid values stop startup. Byte limits use integer bytes and
durations use strings such as `10s`. All limits and intervals must be positive.

| Resource | Per session | Global |
|---|---:|---:|
| Pending QoS 1 deliveries | 1,000 | 100,000 |
| Pending encoded bytes | 16 MiB | 256 MiB |
| Deliveries awaiting PUBACK | 20 | Included in global budgets |

Pending totals include unsent and unacknowledged deliveries, including connected
clean sessions. Each destination is charged the complete encoded PUBLISH size;
shared payloads do not reduce the logical charge. Counters are released once on
PUBACK or session deletion, not when moving messages into the transport queue.
These are not process memory limits: retained messages, connections, session
metadata and transport copies consume additional memory. Sessions have no expiry.

If a local or global budget is exhausted, new deliveries that do not fit are
dropped. Existing deliveries and subscriptions remain intact, and publishers
still receive PUBACK. Other destinations continue independently; a global limit
can also affect healthy subscribers. There is no all-or-nothing fan-out or
capacity reservation for a particular subscriber. Local saturation takes
precedence when recording the reason. QoS 1 therefore does not promise absence
of loss under capacity exhaustion. QoS 0 is never queued offline.

`Store.Snapshot` and `State.Snapshot` expose occupancy, inflight counts, drop
counters and oldest pending age. Aggregate summaries are logged every 30 seconds;
saturation deltas are aggregated once per reason every 10 seconds. Both intervals
are configurable. There is no HTTP metrics endpoint or Client ID metric label.

### Publish and subscribe

With the broker running, start a subscriber in another terminal:

```sh
mosquitto_sub -h localhost -p 1883 -V mqttv311 -t 'demo/#' -q 1 -v
```

Publish a QoS 1 message from a separate terminal:

```sh
mosquitto_pub -h localhost -p 1883 -V mqttv311 -t demo/hello -q 1 -m 'Hello OrbMQ'
```

The subscriber prints `demo/hello Hello OrbMQ`. Add `-r` to the publisher command to retain the message for future matching subscriptions.

### Run tests

```sh
go test ./...
```

### Run package-specific tests

```sh
go test ./internal/protocol/...
go test ./internal/broker/...
go test ./internal/topic/...
go test ./internal/server/...
go test ./internal/session/...
```

### Run benchmarks

No benchmark functions are currently defined in the repository. Once broker benchmarks are added, run them with allocation reporting:

```sh
go test -run='^$' -bench=. -benchmem ./internal/broker/...
```

## Engineering Priorities

- protocol correctness over feature completeness
- incremental evolution over disruptive rewrites
- backward compatibility for MQTT 3.1.1 while MQTT 5 support is introduced
- benchmark-driven performance work
- clean boundaries between broker core, cluster concerns, and management surfaces
- simple, inspectable designs over unnecessary abstraction

## Roadmap

Planned evolution areas include:

- full MQTT 5 protocol support
- durable session storage and session expiry
- QoS 2 support
- TLS, authentication, and authorization
- metrics and observability
- Docker image and containerized deployment
- cluster coordination and multi-node routing
- management API for administration and inspection
- web UI for broker operations

Roadmap order may change as the architecture evolves.

## License

OrbMQ is licensed under the MIT License. See [`LICENSE`](LICENSE) for details.
