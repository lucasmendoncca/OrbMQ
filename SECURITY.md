# Security configuration

OrbMQ supports MQTT 3.1.1 over TLS with local username/password authentication
and topic authorization. TLS authenticates the server; mutual TLS is not yet
supported. There is one listener per process and no configuration reload.

## Modes and migration

Every operational configuration must explicitly select a security mode. Existing
YAML files without `security.mode` now fail startup. For local development, add:

```yaml
security:
  mode: development
server:
  address: "127.0.0.1:1883"
```

Development has no authentication or topic restrictions, logs a startup warning,
and only accepts literal loopback addresses such as `127.0.0.1` or `::1`.
Wildcard addresses and hostnames, including `localhost`, are rejected. Credentials
and TLS fields are rejected in development mode rather than silently ignored.

For access outside the local machine, use:

```yaml
server:
  address: ":8883"
security:
  mode: secure
  credentials_file: security/users.yaml
  tls:
    cert_file: security/server.crt
    key_file: security/server.key
```

All referenced paths are relative to the main YAML file's directory unless
absolute. The certificate file contains a PEM certificate chain, starting with
the server certificate; the key file contains its matching PEM private key.
Provision a certificate for the hostname clients actually use and configure
clients to trust its issuer. TLS 1.2 is the minimum; TLS 1.3 is also supported.
There is no fallback to unencrypted TCP or anonymous access on startup failure.
Use a supported, security-patched Go toolchain for external deployments; the
module's minimum Go version is not a guarantee that its initial patch is safe.

Both YAML files are limited to 64 KiB. Unknown fields, duplicate mapping keys,
nulls, aliases, additional documents and invalid values stop startup before the
listener opens. Protect credentials and private keys with filesystem permissions;
never commit operational credentials or keys.

## Identities and permissions

Create `security/users.yaml` using generated hashes, not plaintext passwords:

```yaml
users:
  - username: sensor
    password_hash: "<replace with generated hash>"
    client_ids:
      - sensor-42
    publish:
      - devices/42/telemetry
      - devices/42/status
    subscribe:
      - devices/42/commands/#
```

Usernames identify principals; Client IDs identify MQTT sessions. Each Client ID
must be listed exactly once across the file, with no wildcards or interpolation.
Users cannot take over another identity's session, even with CleanSession=true.
Authentication and Client ID/Will authorization happen before closing the previous
connection or mutating session state. The same owner may replace its connection
and resume a persistent session with the existing QoS 1 replay semantics.

`publish` contains MQTT filters matched against concrete publication topics.
`subscribe` contains filters that bound the full topic set requested by an
subscription. A requested filter must be entirely covered by one rule: permission
for `devices/42/#` permits `devices/42/commands/+`, but refuses `devices/+/#`.
Rules are not combined to construct coverage; there are no deny rules, roles or
priority ordering. Missing or empty permission lists grant no access.

MQTT `+`, `#`, empty topic levels and `$` topics retain their protocol meaning.
Root wildcards do not grant access to `$SYS`; use an explicit `$SYS/...` rule.
Will topics require publish permission during CONNECT. Retained creation,
replacement and deletion require publish permission before any modification.
Rejected subscriptions do not receive retained messages. Unsubscribe may remove
any subscription belonging to the caller's session.

Bad credentials receive CONNACK 0x04, unauthorized Client IDs/Wills receive 0x05,
and authentication capacity exhaustion receives 0x03. SessionPresent is false on
refusal. Each denied SUBSCRIBE entry receives SUBACK 0x80 independently. A denied
PUBLISH closes the connection without publication effects or PUBACK. An accepted
Will can still be emitted on this unexpected disconnect.

TLS, CONNECT, authentication and CONNACK share the configured handshake deadline
and handshake/connection budgets. Password derivations are bounded by GOMAXPROCS
with immediate rejection instead of a waiting queue. A derivation already running
finishes before its handler exits; cancellation does not spawn background work.
Snapshots and existing aggregate summaries include authentication and authorization
rejection counters. Credentials and hashes are not included in diagnostic errors.

## Password generation

Run `go run ./cmd/orbmq-password` with the exact password bytes on stdin. The tool
accepts no password arguments and prints only a hash followed by a newline.
It preserves whitespace, newlines and binary bytes, and accepts at most 65,535
bytes, matching the MQTT binary-field limit. Do not add a newline unintentionally.

For example, on a POSIX shell, read an existing protected password file:

```sh
go run ./cmd/orbmq-password < /private/path/password.bin
```

Do not type operational secrets into shell command arguments or commit the input
file. Store the resulting hash in `password_hash`. Hashes use standard-library
PBKDF2-HMAC-SHA256 with 600,000 iterations, a random 16-byte salt and a 32-byte
derived key. Unknown users perform an equivalent derivation; comparisons are
constant-time. The serialized format is
`pbkdf2-sha256$600000$<base64 salt>$<base64 derived key>`.

## Clients and rotation

Use MQTT 3.1.1 clients with certificate verification enabled. Configure the CA,
username, password and permitted Client ID in the client's protected configuration,
rather than exposing the password in command arguments. For Mosquitto clients,
use their default per-user configuration files (`$HOME/.config/mosquitto_sub`
on POSIX or `%USERPROFILE%\mosquitto_sub` on Windows), with `-u <username>` and
`-P <password>` on separate lines. Restrict file access to the client owner.
Then run:

```sh
mosquitto_sub -h broker.example.com -p 8883 -V mqttv311 --cafile ca.crt -i sensor-42 -t 'devices/42/commands/#' -q 1 -v
```

Do not disable hostname or certificate verification. Separate simultaneous
publisher/subscriber connections need distinct Client IDs authorized in the file.

Certificates, credentials and permissions are immutable for the process lifetime.
Replace the protected files and restart the broker to apply changes or revoke
access. Restarting disconnects clients and loses all current in-memory sessions,
pending QoS 1 deliveries and retained messages. During one process lifetime,
accepted queued deliveries continue to use the same policy and owner; unsubscribe
does not retract messages already accepted into the session queue.

References: [Go TLS](https://pkg.go.dev/crypto/tls),
[OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html),
[Mosquitto client options](https://mosquitto.org/man/mosquitto_sub-1.html).
