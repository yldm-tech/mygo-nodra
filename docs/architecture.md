# Nodra architecture

Nodra has one state owner and explicit integration boundaries:

```text
                 ┌─ selector subscribers
application ───► Store[T] ─┼─ normal subscribers ─► UI invalidation
                 ├─ History
                 ├─ Persistence
                 └─ Watch channels
```

## Store lifecycle

`New` creates an open store at version zero. `Set` and `Update` publish a new
snapshot. `UpdateErr` restores the previous value and publishes nothing when the
callback returns an error or panics. `Batch` groups nested notifications and publishes
once at the outermost boundary. Concurrent writers may join an open batch; use
`Transaction` for one serialized mutation. A closed store rejects future writes with
`ErrClosed`, removes subscribers and closes active `Watch` streams.

## Notifications

Listeners run synchronously on the writer goroutine after the store lock is
released. This makes a listener safe to call `Get`, `Update`, `Close` or
unsubscribe, while preserving an ordered callback sequence for one writer.
Different concurrent writers may invoke listeners concurrently. Applications
that need serialized side effects should use their own queue or mutex.

`SubscribeWith` can deliver the initial snapshot and can compare values per
subscriber. `SubscribeSelector` is a convenience for comparable projections.

## Persistence

Persistence is explicit. `Storage` owns bytes and `Codec[T]` owns encoding.
`FileStorage` and `JSONCodec[T]` provide an atomic JSON file path out of the box. The
core package does not assume a filesystem, JSON, database or encryption scheme.
`AutoSaveWithErrors` exposes save failures through a bounded error channel;
`AutoSave` is available when an application intentionally wants best-effort
saving.

## MyGo integration

`integrations/mygo` imports MyGo and only connects a store subscription to
`Window.Invalidate`. The core Store remains usable in other Go UI toolkits,
services and tests without importing MyGo.
