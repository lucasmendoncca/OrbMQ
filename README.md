# OrbMQ

OrbMQ is a lightweight MQTT broker written in Go. The project currently focuses on a clean and correct MQTT 3.1.1 implementation, while evolving toward a more production-oriented broker with MQTT 5 support, stronger performance, cluster capabilities, and a management interface.

The repository is intentionally being developed in incremental steps. The current codebase is simpler than the long-term vision, and the roadmap below should not be read as already implemented functionality.

## Current Status

OrbMQ currently provides a functional MQTT 3.1.1 publish/subscribe flow with:

- topic subscriptions with wildcard support
- retained message handling
- concurrent fan-out
- buffered client writes with backpressure handling
- a modular internal architecture around broker, protocol, topic, server, and client responsibilities

Current packet support:

| Packet      | Direction        |
|-------------|------------------|
| CONNECT     | Client -> Broker |
| CONNACK     | Broker -> Client |
| PUBLISH     | Both             |
| SUBSCRIBE   | Client -> Broker |
| SUBACK      | Broker -> Client |
| UNSUBSCRIBE | Client -> Broker |
| UNSUBACK    | Broker -> Client |
| PINGREQ     | Client -> Broker |
| PINGRESP    | Broker -> Client |
| DISCONNECT  | Client -> Broker |

Not implemented yet as complete broker capabilities:

- MQTT 5 support
- persistent sessions
- QoS 1 and QoS 2 flows
- TLS
- clustering
- management API and UI

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
  - wires broker and server
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
- `internal/topic/`
  - topic tree and wildcard matching
  - subscriber lookup for publish routing

## Design Characteristics

Some notable implementation choices in the current broker:

- **Copy-on-write topic tree**
  - topic updates clone and swap the trie, keeping publish reads lock-free
- **Per-client send queue**
  - each client has a buffered queue and dedicated write loop
- **Subscriber abstraction**
  - the broker depends on an interface instead of a concrete client type
- **Allocation-aware publish path**
  - pooling is used to reduce pressure on hot-path subscriber matching

These patterns should continue to guide future work, especially when extending the broker for MQTT 5 and cluster scenarios.

## Getting Started

### Requirements

- Go 1.21 or newer
- an MQTT client such as MQTT Explorer or `mosquitto_pub` / `mosquitto_sub`

### Run the broker

```sh
go run ./cmd/orbmq/main.go
```

The broker listens on port `1883` by default.

### Run tests

```sh
go test ./...
```

### Run package-specific tests

```sh
go test ./internal/protocol/...
go test ./internal/broker/...
go test ./internal/topic/...
```

### Run benchmarks

```sh
go test -bench=. ./internal/broker/...
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
- session lifecycle and expiry handling
- QoS 1 and QoS 2 support
- TLS, authentication, and authorization
- metrics and observability
- Docker image and containerized deployment
- cluster coordination and multi-node routing
- management API for administration and inspection
- web UI for broker operations

Roadmap order may change as the architecture evolves.

## License

OrbMQ is licensed under the MIT License. See [`LICENSE`](LICENSE) for details.
