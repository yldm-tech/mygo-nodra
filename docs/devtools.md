# Local DevTools

DevTools is an optional Go subpackage with an embedded browser inspector. There
is no frontend build step or runtime dependency on Node. Import it only in the
development configuration that should expose the inspector.

```go
inspector := devtools.New(devtools.Options{Limit: 300})
defer inspector.Close()
detach, err := devtools.Attach(inspector, "lookup", lookupStore)
if err != nil {
    return err
}
defer detach()
url, err := inspector.Listen()
if err != nil {
    return err
}
fmt.Println(url)
```

Listen binds an ephemeral port on `127.0.0.1` and returns a session URL. Open the
whole URL, including its fragment: the browser sends its capability as a bearer
header. The fragment is not part of HTTP requests or referrers. API routes reject
missing tokens, cross-origin requests and non-loopback Host values. State
responses are not cached; the UI loads only its embedded local CSS/JavaScript.
Close detaches all subscriptions and shuts down the listener. Each attach also
returns an idempotent detach function. IDs must be unique among current stores.

## Inspecting state and events

- The sidebar lists heterogeneous stores and their current source versions.
- The timeline records initial state, mutations (direct/patch/reset/action) and
  completed actions, with names, durations and errors. Failed actions appear
  without inventing a successful state mutation.
- State, Changes and Event panels show JSON, before/after differences and metadata.
- Filter matches event type, mutation type, action name and error text.
- Export snapshot downloads the current stores and retained event history as JSON.
- Clear timeline removes history while keeping attached stores and current state.

History is bounded (200 events by default). Event sequence numbers describe
capture order; concurrent mutation callbacks can arrive out of source-version
order. The current state only advances to the highest captured version. Action
metadata includes the version observed at notification time; its state panel is
explicitly live rather than a snapshot of the action. Updates during initial
registration may coalesce into a catch-up snapshot.

Snapshots are independent JSON buffers. Unsupported JSON types and redaction
errors appear as inspection errors without preventing store updates. Capturing
and serializing events runs on the notifying goroutine, so use the inspector
where development overhead is acceptable and keep source values immutable or
use NewWithClone.

## JSON editing and state restoration

Pass `AllowRestore: true` to opt in. The inspector then exposes Apply JSON state
and Restore snapshot. Both strictly decode into the attached store's Go type
before Set; malformed JSON, wrong types and unknown struct fields are rejected
without modifying state. Store subscriptions and persistence run normally.
Unsubmitted JSON edits survive polling, including after the editor loses focus.

Restoration creates a new source version and new events. It does not undo network
requests, file operations or other external effects of an action, and it does
not replay action functions. JSON cannot preserve unexported fields, pointer
identity or values excluded by json tags. Attach restorable state only when JSON
replacement is appropriate. An evicted event cannot be restored; an event from a
detached store cannot restore a new store later attached under the same ID.
An already-running Apply may finish while its store is being detached.

## Redacting state

```go
inspector := devtools.New(devtools.Options{
    Redact: func(id string, raw json.RawMessage) (json.RawMessage, error) {
        var value map[string]any
        if err := json.Unmarshal(raw, &value); err != nil {
            return nil, err
        }
        delete(value, "token")
        return json.Marshal(value)
    },
})
```

Redact runs before JSON is retained, including both previous/current mutation
values, initial state and catch-up snapshots. It must be concurrency-safe and
return valid JSON. A redacted inspector is always read-only so masked state can
never be restored. Action names/errors are diagnostic strings and are not passed
through the state redactor; keep sensitive data out of those strings too. Session
URLs and exported debug snapshots should stay with the intended development
session. No automatic publishing or external telemetry is performed.

## Demo and browser verification

```sh
go run ./examples/devtools                 # read-only counter
go run ./examples/devtools -restore        # enable editing/restoration
```

The counter records a successful and rejected action, then increments once per
second. Use `-interval=1h` for a stable manual editing session.

Go tests cover capture, bounded history, independent exports, redaction, strict
restoration, attachment generations, concurrent capture/close, authenticated API
requests and listener shutdown. The optional Playwright smoke test drives the
actual UI through inspection, error details, filtering, state restoration, JSON
validation, retained edits, export, diffs, clear and read-only mode. CI runs it in
headless Chromium:

```sh
# Install test tooling outside the repository; library users do not need Node.
npm install --prefix /tmp/nodra-browser --no-save --package-lock=false playwright@1.63.0
/tmp/nodra-browser/node_modules/.bin/playwright install chromium
PLAYWRIGHT_PATH=/tmp/nodra-browser/node_modules/playwright node scripts/devtools-smoke.mjs
```

`DEVTOOLS_CHROMIUM_PATH` can select an existing Chromium executable.
`DEVTOOLS_SCREENSHOT` optionally saves the exercised UI as a PNG.

![Nodra DevTools timeline and before/after difference](devtools.png)
