# DNS Lab

Build a chain of DNS resolvers, send a real UDP query, and inspect the
timing of each hop in the browser. The resolver also runs as a standalone CLI.

## Requirements

- Go 1.26.5 or newer and a terminal on Linux or macOS.
- A modern browser. The frontend is embedded in the Go controller.
- Loopback TCP and UDP sockets. Internet access is needed only for queries
  that reach the external upstream. `dig` and `nslookup` are optional.

## Setup after cloning

Clone the repository, open a terminal in the checkout, then run:

```bash
mkdir -p bin
go build -o bin/dns .
go build -o bin/lab ./cmd/lab
./bin/lab
```

Open **http://127.0.0.1:8080**. Keep the controller terminal open while using the
lab. To use another HTTP port, run `./bin/lab -listen 127.0.0.1:8081` and open
the address printed in the terminal.

1. Drag nodes to arrange them. Click a node to edit its properties. Drag its
   output handle to another node to set its upstream, or use the selector in
   the properties panel.
2. Click **Run**. Any standalone resolver already using a configured port must
   release it first, or you can change that node's port in the editor.
3. Click **Host**, query `second.test` with type **A**, and select the lookup in
   **Query traces**. Repeat the query to see the first resolver's cache hit.
   Try `third.test` with type **AAAA** to see another hop. Both work offline.
4. **Remake** replaces the running chain with the edited draft and starts with
   fresh caches. **Stop** releases its ports and keeps the controller open.
   **Quit** or **Ctrl+C** stops both the chain and controller. Closing the browser
   tab leaves the controller running.

The editor saves its draft in your browser. For a timing experiment, set the
main resolver's upstream timeout to 500 ms and the second resolver's forwarding
delay to 1,000 ms, then **Remake** and query a name that misses both local record
maps. The caller will give up while the downstream resolver is still waiting.
Click individual spans to inspect transaction IDs, DNS response codes, and
returned records.

## CLI-only setup

Run the resolver in three separate terminals from the repository root:

```bash
# First terminal: client-facing server, with cache
go run . -main -port 5353 -records records.json -upstream 127.0.0.1:5354 -delay 1s

# Second terminal: its own records, no cache
go run . -port 5354 -records records-second.json -upstream 127.0.0.1:5355 -delay 1s

# Third terminal: its own records, then the public resolver
go run . -port 5355 -records records-third.json -upstream 1.1.1.1:53 -delay 1s
```

Each process binds to `127.0.0.1` and handles one query at a time. It sleeps
before each upstream request, then blocks waiting for that request's response.
There are no concurrent request handlers or mutexes. Packets can wait in the OS
receive buffer while a query is being processed.

`-upstream` defaults to the next loopback port. Only the last process needs a
public upstream. `-main` enables caching and requires a loopback upstream.
Failures return SERVFAIL; no process bypasses its configured upstream.
Avoid configuring a forwarding cycle. The default upstream timeout is 10 seconds
per hop, excluding that process's artificial delay; set it with `-timeout`.

## Queries

Use a client timeout long enough for the accumulated delays:

```bash
# Main's own record: no forwarding delay
nslookup -timeout=15 -retry=1 -port=5353 -type=A example.test 127.0.0.1

# Second server: approximately 1 second on the first query
nslookup -timeout=15 -retry=1 -port=5353 -type=A second.test 127.0.0.1

# Third server: approximately 2 seconds on the first query
nslookup -timeout=15 -retry=1 -port=5353 -type=AAAA third.test 127.0.0.1

# Public upstream: approximately 3 seconds plus network time on a cache miss
nslookup -timeout=15 -retry=1 -port=5353 -type=A example.com 127.0.0.1

# Reverse lookup held by the second server
nslookup -timeout=15 -retry=1 -port=5353 -type=PTR 20.2.0.192.in-addr.arpa 127.0.0.1
```

Repeat an identical query to see a cache hit on the main server. Logs show
`local answer`, `forward`, or `cache hit`. Use `-delay 0s` to disable simulation.
Artificial delays apply only to outbound queries, not returning responses.

## Lookup and caching behavior

Local records take precedence over the cache. A known name lacking the requested
type gets an empty successful answer. An unknown name is forwarded when RD is
set; otherwise it receives REFUSED. Upstream NXDOMAIN responses pass through.
Records are loaded and validated at startup; restart to pick up file changes.

The main caches successful, nonempty, untruncated upstream answers. The key
includes the lowercase question name, type, class, flags, and EDNS parameters.
The cache has at most 1024 entries and expires each response at the shortest
ordinary record TTL in that response. Cache hits update the transaction ID and
remaining TTLs. OPT metadata is not decremented. Negative answers are not cached.
Other processes have no cache; local records do not expire.

Forwarded packets stay in their original wire layout to preserve compression
pointers. Responses must match the upstream transaction ID and question.
Local answers set AA; forwarded/cache answers clear AA. RA advertises forwarding
availability, and RD is copied from the client's query.

This exercise supports UDP only: truncated upstream replies are relayed without
TCP fallback. Public lookups require network access to `1.1.1.1:53`.

## Tests

```bash
go test -race ./...
```

Tests use local UDP sockets and a fixture in place of the public resolver. They
cover the forwarding chain, caching and expiry, TTL updates, compressed RDATA,
delay, failure handling, response matching, and A/AAAA/PTR records.
