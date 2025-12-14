# OrbMQ

OrbMQ is a lightweight MQTT broker written in Go, designed as a learning-focused project with a strong emphasis on protocol correctness, clean architecture, and incremental evolution toward a production-grade system.

The project follows the MQTT 3.1.1 specification and currently implements a functional publish/subscribe flow with topic wildcards and concurrent fan-out.

## Features

- MQTT 3.1.1 protocol support (partial)

- TCP-based broker with one goroutine per connection

- CONNECT / CONNACK handshake

- PINGREQ / PINGRESP keepalive handling

- SUBSCRIBE / SUBACK support

- PUBLISH (QoS 0)

- Topic routing with + and # wildcards

- Concurrent fan-out to multiple subscribers

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

- topic

  - Topic tree (trie)

  - Wildcard matching

- client

  - Connection abstraction

  - Thread-safe writes to the network socket
 

## Supported MQTT Packets

| Packet      | Supported | Notes                 |
| ----------- | --------- | --------------------- |
| CONNECT     | Yes       | MQTT 3.1.1 only       |
| CONNACK     | Yes       | Session Present false |
| PINGREQ     | Yes       |                       |
| PINGRESP    | Yes       |                       |
| SUBSCRIBE   | Yes       | QoS 0 only            |
| SUBACK      | Yes       |                       |
| PUBLISH     | Yes       | QoS 0 only            |
| UNSUBSCRIBE | No        | Planned               |
| DISCONNECT  | No        | Planned               |

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

- DISCONNECT handling and subscription cleanup

- Retained messages

- Session management and Clean Session support

- Remaining Length encoding for large payloads

- Metrics and observability

- TLS and authentication

- QoS 1 support

## License

This project is currently provided for experimental purposes.
A formal license will be added in a future revision.
