# Academic maintenance collection

`go run ./cmd/academic-refresh` performs one authorized Chaoxing/MOOC collection pass in one control round. Both adapters share the same paced HTTP client. Each page commits through the existing platform service; the control-round heartbeat remains active throughout the pass. This command does not start Agent workers or collect DWS.

Load the existing `DATABASE_URL` and `CREDENTIAL_KEY` securely before running. Optional CLI settings:

| Variable | Default | Accepted values |
|---|---|---|
| `ACADEMIC_COLLECTION_TIMEOUT` | `30m` | A Go duration greater than zero and at most `4h`; an earlier caller deadline still applies |
| `ACADEMIC_REQUEST_INTERVAL` | `3s` | A Go duration from `500ms` through `30s`; zero is rejected |
| `ACADEMIC_SOURCE` | Both adapters | `chaoxing` or `mooc` to select one source |
| `ACADEMIC_RESOURCES` | All declared resources | Comma-separated declared resource names, only when `ACADEMIC_SOURCE=mooc` |
| `ACADEMIC_COLLECTION_REPORT` | `../.build/academic-collection-report.json` | Report output path; its parent directory must already exist |

For a longer conservative maintenance pass across both adapters:

```sh
ACADEMIC_COLLECTION_TIMEOUT=3h ACADEMIC_REQUEST_INTERVAL=3s go run ./cmd/academic-refresh
```

Requests use one global lane across hosts. Before each transport request, including redirects and requests from restored sessions, the transport waits for the prior request to return response headers and then leaves the configured interval. The lane is released before response-body processing and database storage. Waiting respects request cancellation and the client's existing 40-second timeout. Adapter-specific deadlines also remain in effect (MOOC uses 20 seconds per request), so a large interval can consume that request's deadline before it is sent; the default three seconds leaves time for the request. HTTP errors also consume the interval; this wrapper adds no retries. Authentication, rate-limit, or verification failures keep the existing stop behavior for that connection. The existing collection page budgets and course/resource ordering remain in effect.

These environment variables configure this command only. The ordinary platform `CollectOnce` entry point keeps its 30-minute timeout; explicit maintenance callers can use `CollectOnceWithTimeout` with a positive timeout of at most four hours.

Validation uses in-memory repositories and fake HTTP transports. It covers configuration bounds, request gaps across hosts, failure propagation without retries, cancellation while waiting, and maintenance timeout validation. It does not contact real platforms or a database.
