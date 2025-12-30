# OrbMQ

OrbMQ is a lightweight MQTT broker written in Go, designed as a learning-focused project with a strong emphasis on protocol correctness, clean architecture, and incremental evolution toward a production-grade system.

The project follows the MQTT 3.1.1 specification and currently implements a functional publish/subscribe flow with topic wildcards and concurrent fan-out.

## Features

- MQTT 3.1.1 protocol support (partial)

- TCP-based broker with one goroutine per connection

- CONNECT / CONNACK handshake

- DISCONNECT handling with proper subscription cleanup

- PINGREQ / PINGRESP keepalive handling

- SUBSCRIBE / SUBACK support

- PUBLISH (QoS 0)

- Retained messages support

- Topic routing with + and # wildcards

- Concurrent fan-out to multiple subscribers

- Lock-free publish hot path using copy-on-write state

- Protocol parsing with strict Remaining Length handling

## Architecture Overview

OrbMQ is structured to clearly separate responsibilities:

- server

  - TCP listener

  - Connection lifecycle management

  - Translates protocol events into broker actions

- protocol

  - MQTT packet encoding and decoding

  - Strict binary parsing

  - No business logic

- broker

  - Publish/subscribe coordination

  - Fan-out logic

  - Retained message management

  - Retained message management

- topic

  - Topic tree (trie)

  - Wildcard matching

- client

  - Connection abstraction

  - Buffered, thread-safe writes to the network socket

  - Backpressure handling
 
## Supported MQTT Packets

| Packet      | Supported | Notes                           |
| ----------- | --------- | --------------------------------|
| CONNECT     | Yes       | MQTT 3.1.1 only                 |
| CONNACK     | Yes       | Session Present false           |
| PINGREQ     | Yes       |                                 |
| PINGRESP    | Yes       |                                 |
| SUBSCRIBE   | Yes       | QoS 0 only                      |
| SUBACK      | Yes       |                                 |
| PUBLISH     | Yes       | QoS 0 only                      |
| DISCONNECT  | Yes       | Graceful disconnect and cleanup |
| UNSUBSCRIBE | No        | Planned                         |

## Getting Started
### Requirements

Go 1.21 or newer

An MQTT client (MQTT Explorer or mosquitto)

### Run the broker
```sh
go run .\cmd\orbmq\main.go
``` 

The broker listens on port 1883 by default.

## Design Goals

- Protocol correctness over feature completeness

- Simple concurrency model

- Clear ownership of responsibilities

- Incremental development

- Minimal abstractions, explicit behavior

## Roadmap

Planned next steps:

- UNSUBSCRIBE support

- Session management and Clean Session support

- Remaining Length encoding for large payloads

- Metrics and observability

- Docker image and containerized deployment

- TLS and authentication

- QoS 1 support

## License

OrbMQ is licensed under the MIT License.
See the LICENSE file for details.
