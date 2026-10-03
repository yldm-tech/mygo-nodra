# Nodra architecture

Nodra is the state layer. It owns values, versions, subscriptions and update
coalescing; it does not know about windows, renderers, persistence or IPC.

```text
application state -> nodra.Store[T] -> subscribers -> UI invalidation
                                  -> selector subscribers
                                  -> context-bound Watch channels
```

`Set` and `Update` publish synchronously after releasing the store lock. A
subscriber may safely read or update the same store. `Batch` defers publication
until the outermost batch returns and publishes one snapshot. `Watch` is a
bounded, best effort stream for consumers that prefer channels; slow consumers
may skip intermediate snapshots.

Nodra intentionally does not deep-copy generic values. Applications should use
value-oriented state or copy maps and slices at their mutation boundary.

The `integrations/mygo` package is an adapter, not part of the core. It connects
a store to `Window.Invalidate` so native MyGo views can rebuild on state changes.
